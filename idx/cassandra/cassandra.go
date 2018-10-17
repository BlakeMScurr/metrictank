package cassandra

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/raintank/dur"

	"github.com/gocql/gocql"
	"github.com/grafana/metrictank/cassandra"
	"github.com/grafana/metrictank/cluster"
	"github.com/grafana/metrictank/idx"
	"github.com/grafana/metrictank/idx/memory"
	"github.com/grafana/metrictank/stats"
	"github.com/grafana/metrictank/util"
	"github.com/raintank/schema"
	log "github.com/sirupsen/logrus"
)

var (
	// metric idx.cassadra.query-insert.ok is how many insert queries for a metric completed successfully (triggered by an add or an update)
	statQueryInsertOk = stats.NewCounter32("idx.cassandra.query-insert.ok")
	// metric idx.cassandra.query-insert.fail is how many insert queries for a metric failed (triggered by an add or an update)
	statQueryInsertFail = stats.NewCounter32("idx.cassandra.query-insert.fail")
	// metric idx.cassadra.query-delete.ok is how many delete queries for a metric completed successfully (triggered by an update or a delete)
	statQueryDeleteOk = stats.NewCounter32("idx.cassandra.query-delete.ok")
	// metric idx.cassandra.query-delete.fail is how many delete queries for a metric failed (triggered by an update or a delete)
	statQueryDeleteFail = stats.NewCounter32("idx.cassandra.query-delete.fail")

	// metric idx.cassandra.query-insert.wait is time inserts spent in queue before being executed
	statQueryInsertWaitDuration = stats.NewLatencyHistogram12h32("idx.cassandra.query-insert.wait")
	// metric idx.cassandra.query-insert.exec is time spent executing inserts (possibly repeatedly until success)
	statQueryInsertExecDuration = stats.NewLatencyHistogram15s32("idx.cassandra.query-insert.exec")
	// metric idx.cassandra.query-delete.exec is time spent executing deletes (possibly repeatedly until success)
	statQueryDeleteExecDuration = stats.NewLatencyHistogram15s32("idx.cassandra.query-delete.exec")

	// metric idx.cassandra.add is the duration of an add of one metric to the cassandra idx, including the add to the in-memory index, excluding the insert query
	statAddDuration = stats.NewLatencyHistogram15s32("idx.cassandra.add")
	// metric idx.cassandra.update is the duration of an update of one metric to the cassandra idx, including the update to the in-memory index, excluding any insert/delete queries
	statUpdateDuration = stats.NewLatencyHistogram15s32("idx.cassandra.update")
	// metric idx.cassandra.prune is the duration of a prune of the cassandra idx, including the prune of the in-memory index and all needed delete queries
	statPruneDuration = stats.NewLatencyHistogram15s32("idx.cassandra.prune")
	// metric idx.cassandra.delete is the duration of a delete of one or more metrics from the cassandra idx, including the delete from the in-memory index and the delete query
	statDeleteDuration = stats.NewLatencyHistogram15s32("idx.cassandra.delete")
	// metric idx.cassandra.save.skipped is how many saves have been skipped due to the writeQueue being full
	statSaveSkipped = stats.NewCounter32("idx.cassandra.save.skipped")
	errmetrics      = cassandra.NewErrMetrics("idx.cassandra")

	timeout          time.Duration
	maxStale         time.Duration
	pruneInterval    time.Duration
	updateInterval   time.Duration
	updateInterval32 uint32
	updateCassIdx    bool
)

type writeReq struct {
	def      *schema.MetricDefinition
	recvTime time.Time
}

// Implements the the "MetricIndex" interface
type CasIdx struct {
	memory.MemoryIdx
	cluster    *gocql.ClusterConfig
	session    *gocql.Session
	writeQueue chan writeReq
	wg         sync.WaitGroup
}

type cqlIterator interface {
	Scan(dest ...interface{}) bool
	Close() error
}

func New() *CasIdx {
	cluster := gocql.NewCluster(strings.Split(CasIdxConfig.hosts, ",")...)
	cluster.Consistency = gocql.ParseConsistency(CasIdxConfig.consistency)
	cluster.Timeout = timeout
	cluster.ConnectTimeout = cluster.Timeout
	cluster.NumConns = CasIdxConfig.numConns
	cluster.ProtoVersion = CasIdxConfig.protoVer
	cluster.DisableInitialHostLookup = CasIdxConfig.disableInitialHostLookup
	if CasIdxConfig.ssl {
		cluster.SslOpts = &gocql.SslOptions{
			CaPath:                 CasIdxConfig.capath,
			EnableHostVerification: CasIdxConfig.hostverification,
		}
	}
	if CasIdxConfig.auth {
		cluster.Authenticator = gocql.PasswordAuthenticator{
			Username: CasIdxConfig.username,
			Password: CasIdxConfig.password,
		}
	}

	idx := &CasIdx{
		MemoryIdx: *memory.New(),
		cluster:   cluster,
	}
	if CasIdxConfig.updateCassIdx {
		idx.writeQueue = make(chan writeReq, CasIdxConfig.writeQueueSize)
	}
	updateInterval32 = uint32(updateInterval.Nanoseconds() / int64(time.Second))
	return idx
}

// InitBare makes sure the keyspace, tables, and index exists in cassandra and creates a session
func (c *CasIdx) InitBare() error {
	var err error
	tmpSession, err := c.cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("failed to create cassandra session: %s", err)
	}

	// read templates
	schemaKeyspace := util.ReadEntry(CasIdxConfig.schemaFile, "schema_keyspace").(string)
	schemaTable := util.ReadEntry(CasIdxConfig.schemaFile, "schema_table").(string)

	// create the keyspace or ensure it exists
	if CasIdxConfig.createKeyspace {
		log.Infof("cassandra-idx: ensuring that keyspace %s exist.", CasIdxConfig.keyspace)
		err = tmpSession.Query(fmt.Sprintf(schemaKeyspace, CasIdxConfig.keyspace)).Exec()
		if err != nil {
			return fmt.Errorf("failed to initialize cassandra keyspace: %s", err)
		}
		log.Info("cassandra-idx: ensuring that table metric_idx exist.")
		err = tmpSession.Query(fmt.Sprintf(schemaTable, CasIdxConfig.keyspace)).Exec()
		if err != nil {
			return fmt.Errorf("failed to initialize cassandra table: %s", err)
		}
	} else {
		var keyspaceMetadata *gocql.KeyspaceMetadata
		for attempt := 1; attempt > 0; attempt++ {
			keyspaceMetadata, err = tmpSession.KeyspaceMetadata(CasIdxConfig.keyspace)
			if err != nil {
				if attempt >= 5 {
					return fmt.Errorf("cassandra keyspace not found. %d attempts", attempt)
				}
				log.Warnf("cassandra-idx: cassandra keyspace not found. retrying in 5s. attempt: %d", attempt)
				time.Sleep(5 * time.Second)
			} else {
				if _, ok := keyspaceMetadata.Tables["metric_idx"]; ok {
					break
				} else {
					if attempt >= 5 {
						return fmt.Errorf("cassandra table not found. %d attempts", attempt)
					}
					log.Warnf("cassandra-idx: cassandra table not found. retrying in 5s. attempt: %d", attempt)
					time.Sleep(5 * time.Second)
				}
			}
		}

	}

	tmpSession.Close()
	c.cluster.Keyspace = CasIdxConfig.keyspace
	session, err := c.cluster.CreateSession()
	if err != nil {
		return fmt.Errorf("failed to create cassandra session: %s", err)
	}

	c.session = session

	return nil
}

// Init makes sure the needed keyspace, table, index in cassandra exists, creates the session,
// rebuilds the in-memory index, sets up write queues, metrics and pruning routines
func (c *CasIdx) Init() error {
	tm, tmErr := dur.ParseDuration(CasIdxConfig.timeoutStr)
	if tmErr != nil {
		log.Fatalf("cassandra-idx: failed to parse timeout (%s), %s", CasIdxConfig.timeoutStr, tmErr)
	}
	CasIdxConfig.timeout = tm
	timeout = time.Duration(tm) * time.Second

	ui, uiErr := dur.ParseDuration(CasIdxConfig.updateIntervalStr)
	if uiErr != nil {
		log.Fatalf("cassandra-idx: failed to parse update-internval (%s), %s", CasIdxConfig.updateIntervalStr, uiErr)
	}
	CasIdxConfig.updateInterval = ui
	updateInterval = time.Duration(ui) * time.Second

	ms, msErr := dur.ParseDuration(CasIdxConfig.maxStaleStr)
	if msErr != nil {
		log.Fatalf("cassandra-idx: failed to parse max-stale (%s), %s", CasIdxConfig.maxStaleStr, msErr)
	}
	CasIdxConfig.maxStale = ms
	maxStale = time.Duration(ms) * time.Second

	pi, piErr := dur.ParseDuration(CasIdxConfig.pruneIntervalStr)
	if piErr != nil {
		log.Fatalf("cassandra-idx: failed to prase prune-interval (%s), %s", CasIdxConfig.pruneIntervalStr, piErr)
	}
	CasIdxConfig.pruneInterval = pi
	pruneInterval = time.Duration(pi) * time.Second

	updateCassIdx = CasIdxConfig.updateCassIdx

	log.Infof("initializing cassandra-idx. Hosts=%s", CasIdxConfig.hosts)
	if err := c.MemoryIdx.Init(); err != nil {
		return err
	}

	if err := c.InitBare(); err != nil {
		return err
	}

	if updateCassIdx {
		c.wg.Add(CasIdxConfig.numConns)
		for i := 0; i < CasIdxConfig.numConns; i++ {
			go c.processWriteQueue()
		}
		log.Infof("cassandra-idx: started %d writeQueue handlers", CasIdxConfig.numConns)
	}

	//Rebuild the in-memory index.
	c.rebuildIndex()

	if maxStale > 0 {
		if pruneInterval == 0 {
			return fmt.Errorf("pruneInterval must be greater then 0")
		}
		go c.prune()
	}
	return nil
}

func (c *CasIdx) Stop() {
	log.Info("cassandra-idx: stopping")
	c.MemoryIdx.Stop()

	// if updateCassIdx is disabled then writeQueue should never have been initialized
	if updateCassIdx {
		close(c.writeQueue)
	}
	c.wg.Wait()
	c.session.Close()
}

// Update updates an existing archive, if found.
// It returns whether it was found, and - if so - the (updated) existing archive and its old partition
func (c *CasIdx) Update(point schema.MetricPoint, partition int32) (idx.Archive, int32, bool) {
	pre := time.Now()

	archive, oldPartition, inMemory := c.MemoryIdx.Update(point, partition)

	if !updateCassIdx {
		statUpdateDuration.Value(time.Since(pre))
		return archive, oldPartition, inMemory
	}

	if inMemory {
		// Cassandra uses partition id as the partitioning key, so an "update" that changes the partition for
		// an existing metricDef will just create a new row in the table and wont remove the old row.
		// So we need to explicitly delete the old entry.
		if oldPartition != partition {
			c.deleteDefAsync(point.MKey, oldPartition)
		}

		// check if we need to save to cassandra.
		now := uint32(time.Now().Unix())
		if archive.LastSave < (now - updateInterval32) {
			archive = c.updateCassandra(now, inMemory, archive, partition)
		}
	}

	statUpdateDuration.Value(time.Since(pre))
	return archive, oldPartition, inMemory
}

func (c *CasIdx) AddOrUpdate(mkey schema.MKey, data *schema.MetricData, partition int32) (idx.Archive, int32, bool) {
	pre := time.Now()

	archive, oldPartition, inMemory := c.MemoryIdx.AddOrUpdate(mkey, data, partition)

	stat := statUpdateDuration
	if !inMemory {
		stat = statAddDuration
	}

	if !updateCassIdx {
		stat.Value(time.Since(pre))
		return archive, oldPartition, inMemory
	}

	if inMemory {
		// Cassandra uses partition id as the partitioning key, so an "update" that changes the partition for
		// an existing metricDef will just create a new row in the table and wont remove the old row.
		// So we need to explicitly delete the old entry.
		if oldPartition != partition {
			c.deleteDefAsync(mkey, oldPartition)
		}
	}

	// check if we need to save to cassandra.
	now := uint32(time.Now().Unix())
	if archive.LastSave < (now - updateInterval32) {
		archive = c.updateCassandra(now, inMemory, archive, partition)
	}

	stat.Value(time.Since(pre))
	return archive, oldPartition, inMemory
}

// updateCassandra saves the archive to cassandra and
// updates the memory index with the updated fields.
func (c *CasIdx) updateCassandra(now uint32, inMemory bool, archive idx.Archive, partition int32) idx.Archive {

	// if the entry has not been saved for 1.5x updateInterval
	// then perform a blocking save.
	if archive.LastSave < (now - updateInterval32 - updateInterval32/2) {
		log.Debug("cassandra-idx: updating def in index.")
		c.writeQueue <- writeReq{recvTime: time.Now(), def: &archive.MetricDefinition}
		archive.LastSave = now
		c.MemoryIdx.UpdateArchive(archive)
	} else {
		// perform a non-blocking write to the writeQueue. If the queue is full, then
		// this will fail and we wont update the LastSave timestamp. The next time
		// the metric is seen, the previous lastSave timestamp will still be in place and so
		// we will try and save again.  This will continue until we are successful or the
		// lastSave timestamp become more then 1.5 x UpdateInterval, in which case we will
		// do a blocking write to the queue.
		select {
		case c.writeQueue <- writeReq{recvTime: time.Now(), def: &archive.MetricDefinition}:
			archive.LastSave = now
			c.MemoryIdx.UpdateArchive(archive)
		default:
			statSaveSkipped.Inc()
			log.Debug("writeQueue is full, update not saved.")
		}
	}

	return archive
}

func (c *CasIdx) rebuildIndex() {
	log.Info("cassandra-idx: Rebuilding Memory Index from metricDefinitions in Cassandra")
	pre := time.Now()
	var defs []schema.MetricDefinition
	var staleTs uint32
	if maxStale != 0 {
		staleTs = uint32(time.Now().Add(maxStale * -1).Unix())
	}
	defs = c.LoadPartitions(cluster.Manager.GetPartitions(), defs, staleTs)

	num := c.MemoryIdx.Load(defs)
	log.Infof("cassandra-idx: Rebuilding Memory Index Complete. Imported %d. Took %s", num, time.Since(pre))
}

func (c *CasIdx) Load(defs []schema.MetricDefinition, cutoff uint32) []schema.MetricDefinition {
	iter := c.session.Query("SELECT id, orgid, partition, name, interval, unit, mtype, tags, lastupdate from metric_idx").Iter()
	return c.load(defs, iter, cutoff)
}

func (c *CasIdx) LoadPartitions(partitions []int32, defs []schema.MetricDefinition, cutoff uint32) []schema.MetricDefinition {
	placeholders := make([]string, len(partitions))
	for i, p := range partitions {
		placeholders[i] = strconv.Itoa(int(p))
	}
	q := fmt.Sprintf("SELECT id, orgid, partition, name, interval, unit, mtype, tags, lastupdate from metric_idx where partition in (%s)", strings.Join(placeholders, ","))
	iter := c.session.Query(q).Iter()
	return c.load(defs, iter, cutoff)
}

func (c *CasIdx) load(defs []schema.MetricDefinition, iter cqlIterator, cutoff uint32) []schema.MetricDefinition {
	defsByNames := make(map[string][]*schema.MetricDefinition)
	var id, name, unit, mtype string
	var orgId, interval int
	var partition int32
	var lastupdate int64
	var tags []string
	cutoff64 := int64(cutoff)
	for iter.Scan(&id, &orgId, &partition, &name, &interval, &unit, &mtype, &tags, &lastupdate) {
		mkey, err := schema.MKeyFromString(id)
		if err != nil {
			log.Errorf("cassandra-idx: load() could not parse ID %q: %s -> skipping", id, err)
			continue
		}
		if orgId < 0 {
			orgId = int(idx.OrgIdPublic)
		}

		mdef := &schema.MetricDefinition{
			Id:         mkey,
			OrgId:      uint32(orgId),
			Partition:  partition,
			Name:       name,
			Interval:   interval,
			Unit:       unit,
			Mtype:      mtype,
			Tags:       tags,
			LastUpdate: lastupdate,
		}
		nameWithTags := mdef.NameWithTags()
		defsByNames[nameWithTags] = append(defsByNames[nameWithTags], mdef)
	}
	if err := iter.Close(); err != nil {
		log.Fatalf("Could not close iterator: %s", err.Error())
	}

NAMES:
	for name, defsByName := range defsByNames {
		for _, def := range defsByName {
			if def.LastUpdate >= cutoff64 {
				// if one of the defs in a name is not stale, then we'll need to add
				// all the associated MDs to the defs slice
				for _, defToAdd := range defsByNames[name] {
					defs = append(defs, *defToAdd)
				}
				continue NAMES
			}
		}
	}

	return defs
}

func (c *CasIdx) processWriteQueue() {
	var success bool
	var attempts int
	var err error
	var req writeReq
	qry := `INSERT INTO metric_idx (id, orgid, partition, name, interval, unit, mtype, tags, lastupdate) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	for req = range c.writeQueue {
		if err != nil {
			log.Errorf("Failed to marshal metricDef. %s", err)
			continue
		}
		statQueryInsertWaitDuration.Value(time.Since(req.recvTime))
		pre := time.Now()
		success = false
		attempts = 0

		for !success {
			if err := c.session.Query(
				qry,
				req.def.Id.String(),
				req.def.OrgId,
				req.def.Partition,
				req.def.Name,
				req.def.Interval,
				req.def.Unit,
				req.def.Mtype,
				req.def.Tags,
				req.def.LastUpdate).Exec(); err != nil {

				statQueryInsertFail.Inc()
				errmetrics.Inc(err)
				if (attempts % 20) == 0 {
					log.Warnf("cassandra-idx: Failed to write def to cassandra. it will be retried. %s", err)
				}
				sleepTime := 100 * attempts
				if sleepTime > 2000 {
					sleepTime = 2000
				}
				time.Sleep(time.Duration(sleepTime) * time.Millisecond)
				attempts++
			} else {
				success = true
				statQueryInsertExecDuration.Value(time.Since(pre))
				statQueryInsertOk.Inc()
				log.Debugf("cassandra-idx: metricDef saved to cassandra. %s", req.def.Id)
			}
		}
	}
	log.Info("cassandra-idx: writeQueue handler ended.")
	c.wg.Done()
}

func (c *CasIdx) Delete(orgId uint32, pattern string) ([]idx.Archive, error) {
	pre := time.Now()
	defs, err := c.MemoryIdx.Delete(orgId, pattern)
	if err != nil {
		return defs, err
	}
	if updateCassIdx {
		for _, def := range defs {
			err = c.deleteDef(def.Id, def.Partition)
			if err != nil {
				log.Errorf("cassandra-idx: %s", err.Error())
			}
		}
	}
	statDeleteDuration.Value(time.Since(pre))
	return defs, err
}

func (c *CasIdx) deleteDef(key schema.MKey, part int32) error {
	pre := time.Now()
	attempts := 0
	keyStr := key.String()
	for attempts < 5 {
		attempts++
		err := c.session.Query("DELETE FROM metric_idx where partition=? AND id=?", part, keyStr).Exec()
		if err != nil {
			statQueryDeleteFail.Inc()
			errmetrics.Inc(err)
			log.Errorf("cassandra-idx: Failed to delete metricDef %s from cassandra. %s", keyStr, err)
			time.Sleep(time.Second)
		} else {
			statQueryDeleteOk.Inc()
			statQueryDeleteExecDuration.Value(time.Since(pre))
			return nil
		}
	}
	return fmt.Errorf("unable to delete metricDef %s from index after %d attempts.", keyStr, attempts)
}

func (c *CasIdx) deleteDefAsync(key schema.MKey, part int32) {
	go func() {
		if err := c.deleteDef(key, part); err != nil {
			log.Errorf(err.Error())
		}
	}()
}

func (c *CasIdx) Prune(oldest time.Time) ([]idx.Archive, error) {
	pre := time.Now()
	pruned, err := c.MemoryIdx.Prune(oldest)
	statPruneDuration.Value(time.Since(pre))
	return pruned, err
}

func (c *CasIdx) prune() {
	ticker := time.NewTicker(pruneInterval)
	msStr := maxStale.String()
	msNeg := maxStale * -1
	for range ticker.C {
		log.Debugf("cassandra-idx: pruning items from index that have not been seen for %s", msStr)
		staleTs := time.Now().Add(msNeg)
		_, err := c.Prune(staleTs)
		if err != nil {
			log.Errorf("cassandra-idx: prune error. %s", err)
		}
	}
}
