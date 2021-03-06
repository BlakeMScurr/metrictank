package memory

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/grafana/metrictank/idx"
	"github.com/grafana/metrictank/test"
	"github.com/raintank/schema"
)

var ids []schema.MKey

func getTestIDs(t *testing.T) []schema.MKey {
	if len(ids) > 0 {
		return ids
	}

	ids = []schema.MKey{
		test.GetMKey(0),
		test.GetMKey(1),
		test.GetMKey(2),
		test.GetMKey(3),
		test.GetMKey(4),
		test.GetMKey(5),
		test.GetMKey(6),
		test.GetMKey(7),
	}

	return ids
}

func getTestIndex(t *testing.T) (TagIndex, map[schema.MKey]*idx.Archive) {
	type testCase struct {
		id         schema.MKey
		lastUpdate int64
		tags       []string
	}

	ids := getTestIDs(t)

	data := []testCase{
		{ids[0], 1, []string{"key1=value1", "key2=value2"}},
		{ids[1], 2, []string{"key1=value1", "key3=value3"}},
		{ids[2], 3, []string{"key1=value1", "key4=value4"}},
		{ids[3], 4, []string{"key1=value1", "abc=bbb", "key4=value3", "key3=value3"}},
		{ids[4], 5, []string{"key2=value1", "key5=value4", "key3=value3"}},
		{ids[5], 6, []string{"key2=value2", "aaa=bbb", "abc=bbb", "key4=value5", "key3=vabc"}},
		{ids[6], 7, []string{"key3=value1", "aaa=bbb", "key4=value4"}},
		{ids[7], 8, []string{"key3=valxxx"}},
	}

	tagIdx := make(TagIndex)
	byId := make(map[schema.MKey]*idx.Archive)

	for i, d := range data {
		byId[d.id] = &idx.Archive{}
		byId[d.id].Name = fmt.Sprintf("metric%d", i)
		byId[d.id].Tags = d.tags
		byId[d.id].LastUpdate = d.lastUpdate
		for _, tag := range d.tags {
			tagSplits := strings.Split(tag, "=")
			tagIdx.addTagId(tagSplits[0], tagSplits[1], d.id)
		}
		tagIdx.addTagId("name", byId[d.id].Name, d.id)
	}

	return tagIdx, byId
}

func queryAndCompareTagResults(t *testing.T, q TagQuery, expectedData map[string]struct{}) {
	t.Helper()
	tagIdx, byId := getTestIndex(t)

	res := q.RunGetTags(tagIdx, byId)
	if !reflect.DeepEqual(expectedData, res) {
		t.Fatalf("Expected: %+v\nGot: %+v", expectedData, res)
	}
}

func queryAndCompareResults(t *testing.T, q TagQuery, expectedData IdSet) {
	t.Helper()
	tagIdx, byId := getTestIndex(t)

	res := q.Run(tagIdx, byId)

	if !reflect.DeepEqual(expectedData, res) {
		t.Fatalf("Returned data does not match expected data:\nExpected: %s\nGot: %s", expectedData, res)
	}
}

func TestQueryByTagSimpleEqual(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key3=value3"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagSimplePrefix(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key3^=val"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	expect[ids[4]] = struct{}{}
	expect[ids[6]] = struct{}{}
	expect[ids[7]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagPrefixSpecialCaseName(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "name^=metric2"}, 0)
	expect := make(IdSet)
	expect[ids[2]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByPrefix(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key3^=val"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagSimplePattern(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key4=~value[43]", "key3=~value[1-3]"}, 0)
	expect := make(IdSet)
	expect[ids[6]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagSimpleUnequal(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key4!=value4"}, 0)
	expect := make(IdSet)
	expect[ids[0]] = struct{}{}
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagSimpleNotPattern(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=~value?", "key4!=~value[0-9]", "key2!=~va.+"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagWithEqualEmpty(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key2=", "key2=~"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[2]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagWithUnequalEmpty(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key3!=", "key3!=~"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagNameEquals(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key3=value3", "name=metric1"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagNameRegex(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"key1=value1", "key3=value3", "name=~metr"}, 0)
	expect := make(IdSet)
	expect[ids[1]] = struct{}{}
	expect[ids[3]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagMatch(t *testing.T) {
	q, _ := NewTagQuery([]string{"__tag=~a{1}"}, 0)
	expectTags := make(map[string]struct{})
	expectTags["abc"] = struct{}{}
	expectTags["aaa"] = struct{}{}
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag=~a{2}"}, 0)
	delete(expectTags, "abc")
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag=~a{3}"}, 0)
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag=~a{4}"}, 0)
	delete(expectTags, "aaa")
	queryAndCompareTagResults(t, q, expectTags)
}

func TestQueryByTagFilterByTagPrefix(t *testing.T) {
	q, _ := NewTagQuery([]string{"__tag^=a"}, 0)
	expectTags := make(map[string]struct{})
	expectTags["abc"] = struct{}{}
	expectTags["aaa"] = struct{}{}
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag^=aa"}, 0)
	delete(expectTags, "abc")
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag^=aaa"}, 0)
	queryAndCompareTagResults(t, q, expectTags)

	q, _ = NewTagQuery([]string{"__tag^=aaaa"}, 0)
	delete(expectTags, "aaa")
	queryAndCompareTagResults(t, q, expectTags)
}

func TestQueryByTagFilterByTagPrefixSpecialCaseName(t *testing.T) {
	q, _ := NewTagQuery([]string{"key1=value1", "__tag^=na"}, 0)
	expectTags := make(map[string]struct{})
	expectTags["name"] = struct{}{}
	queryAndCompareTagResults(t, q, expectTags)
}

func TestQueryByTagFilterByTagMatchWithExpressionAndNameException(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"__tag=~na", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	expect := make(IdSet)
	expect[ids[0]] = struct{}{}
	expect[ids[5]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagMatchWithExpression(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"__tag=~a{1}", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}

	expect := make(IdSet)
	expect[ids[5]] = struct{}{}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag=~a{2}", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag=~a{3}", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag=~a{4}", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	delete(expect, ids[5])
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagPrefixWithExpression(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"__tag^=aa", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}

	expect := make(IdSet)
	expect[ids[5]] = struct{}{}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag^=aaa", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag^=aaaa", "key2=value2"}, 0)
	if q.startWith != EQUAL {
		t.Fatalf("Expected query to start with equal expression")
	}
	delete(expect, ids[5])
	delete(expect, ids[6])
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagPrefixWithFrom(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"__tag^=aa"}, 7)
	expect := make(IdSet)
	expect[ids[6]] = struct{}{}
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagPrefixWithDifferentLengths(t *testing.T) {
	ids := getTestIDs(t)
	q, _ := NewTagQuery([]string{"__tag^=a"}, 0)
	expect := make(IdSet)
	expect[ids[3]] = struct{}{}
	expect[ids[5]] = struct{}{}
	expect[ids[6]] = struct{}{}
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag^=aaa"}, 0)
	delete(expect, ids[3])
	queryAndCompareResults(t, q, expect)

	q, _ = NewTagQuery([]string{"__tag^=aaaa"}, 0)
	delete(expect, ids[5])
	delete(expect, ids[6])
	queryAndCompareResults(t, q, expect)
}

func TestQueryByTagFilterByTagPrefixWithEmptyString(t *testing.T) {
	_, err := NewTagQuery([]string{"__tag^="}, 0)
	if err != errInvalidQuery {
		t.Fatalf("Expected an error, but didn't get it")
	}
}

func TestQueryByTagInvalidQuery(t *testing.T) {
	_, err := NewTagQuery([]string{"key!=value1"}, 0)
	if err != errInvalidQuery {
		t.Fatalf("Expected an error, but didn't get it")
	}
}

func TestTagExpressionQueryByTagWithFrom(t *testing.T) {
	tagIdx, byId := getTestIndex(t)

	q, _ := NewTagQuery([]string{"key1=value1"}, 4)
	res := q.Run(tagIdx, byId)
	if len(res) != 1 {
		t.Fatalf("Expected %d results, but got %d", 1, len(res))
	}

	q, _ = NewTagQuery([]string{"key1=value1"}, 3)
	res = q.Run(tagIdx, byId)
	if len(res) != 2 {
		t.Fatalf("Expected %d results, but got %d", 2, len(res))
	}

	q, _ = NewTagQuery([]string{"key1=value1"}, 2)
	res = q.Run(tagIdx, byId)
	if len(res) != 3 {
		t.Fatalf("Expected %d results, but got %d", 3, len(res))
	}

	q, _ = NewTagQuery([]string{"key1=value1"}, 1)
	res = q.Run(tagIdx, byId)
	if len(res) != 4 {
		t.Fatalf("Expected %d results, but got %d", 4, len(res))
	}
}

func TestGetByTag(t *testing.T) {
	withAndWithoutPartitonedIndex(testGetByTag)(t)
}

func testGetByTag(t *testing.T) {
	_tagSupport := TagSupport
	defer func() { TagSupport = _tagSupport }()
	TagSupport = true

	ix := New()
	ix.Init()

	idString := "1.000000000000000000000000000000%02x"
	mds := make([]schema.MetricData, 20)
	ids := make([]string, 20)
	for i := range mds {
		ids[i] = fmt.Sprintf(idString, i)
		mds[i].Name = fmt.Sprintf("metric.%d", i)
		mds[i].Id = ids[i]
		mds[i].OrgId = 1
		mds[i].Interval = 1
		mds[i].Time = 12345
	}
	mds[1].Tags = []string{"key1=value1", "key2=value2"}
	mds[11].Tags = []string{"key1=value1"}
	mds[18].Tags = []string{"key1=value2", "key2=value2"}
	mds[3].Tags = []string{"key1=value1", "key3=value3"}

	for i, md := range mds {
		mds[i].SetId()
		mkey, err := schema.MKeyFromString(mds[i].Id)
		if err != nil {
			t.Fatal(err)
		}
		ix.AddOrUpdate(mkey, &md, 1)
	}

	type testCase struct {
		expressions []string
		expectation []string
	}

	fullName := func(md schema.MetricData) string {
		name := md.Name
		sort.Strings(md.Tags)
		for _, tag := range md.Tags {
			name += ";"
			name += tag
		}
		return name
	}

	testCases := []testCase{
		{
			expressions: []string{"key1=value1"},
			expectation: []string{fullName(mds[1]), fullName(mds[11]), fullName(mds[3])},
		}, {
			expressions: []string{"key1=value2"},
			expectation: []string{fullName(mds[18])},
		}, {
			expressions: []string{"key1=~value[0-9]"},
			expectation: []string{fullName(mds[1]), fullName(mds[11]), fullName(mds[18]), fullName(mds[3])},
		}, {
			expressions: []string{"key1=~value[23]"},
			expectation: []string{fullName(mds[18])},
		}, {
			expressions: []string{"key1=value1", "key2=value1"},
			expectation: []string{},
		}, {
			expressions: []string{"key1=value1", "key2=value2"},
			expectation: []string{fullName(mds[1])},
		}, {
			expressions: []string{"key1=~value[12]", "key2=value2"},
			expectation: []string{fullName(mds[1]), fullName(mds[18])},
		}, {
			expressions: []string{"key1=~value1", "key1=value2"},
			expectation: []string{},
		}, {
			expressions: []string{"key1=~value[0-9]", "key2=~", "key3!=value3"},
			expectation: []string{fullName(mds[11])},
		}, {
			expressions: []string{"key2=", "key1=value1"},
			expectation: []string{fullName(mds[11]), fullName(mds[3])},
		},
	}

	for _, tc := range testCases {
		tagQuery, err := NewTagQuery(tc.expressions, 0)
		if err != nil {
			t.Fatalf("Got an unexpected error with query %s: %s", tc.expressions, err)
		}
		res := ix.idsByTagQuery(1, tagQuery)
		if len(tc.expectation) != len(res) {
			t.Fatalf("Result does not match expectation for expressions %+v\nExpected:\n%+v\nGot:\n%+v\n", tc.expressions, tc.expectation, res)
		}

		resPaths := make([]string, 0, len(res))
		for id := range res {
			def, ok := ix.Get(id)
			if !ok {
				t.Fatalf("Tag query returned ID that did not exist in DefByID: %s", id)
			}
			resPaths = append(resPaths, def.NameWithTags())
		}
		sort.Strings(tc.expectation)
		sort.Strings(resPaths)
		if !reflect.DeepEqual(resPaths, tc.expectation) {
			t.Fatalf("Result does not match expectation\nGot:\n%+v\nExpected:\n%+v\n", res, tc.expectation)
		}
	}
}

func TestExpressionParsing(t *testing.T) {
	type testCase struct {
		expression string
		key        string
		value      string
		operator   match
		err        error
	}

	testCases := []testCase{
		{
			expression: "key=value",
			key:        "key",
			value:      "value",
			operator:   EQUAL,
			err:        nil,
		}, {
			expression: "key!=",
			key:        "key",
			value:      "",
			operator:   NOT_EQUAL,
			err:        nil,
		}, {
			expression: "key=",
			key:        "key",
			value:      "",
			operator:   EQUAL,
			err:        nil,
		}, {
			expression: "key=~",
			key:        "key",
			value:      "",
			operator:   MATCH,
			err:        nil,
		}, {
			expression: "key=~v_alue",
			key:        "key",
			value:      "v_alue",
			operator:   MATCH,
			err:        nil,
		}, {
			expression: "k!=~v",
			key:        "k",
			value:      "v",
			operator:   NOT_MATCH,
			err:        nil,
		}, {
			expression: "key!!=value",
			err:        errInvalidQuery,
		}, {
			expression: "key==value",
			key:        "key",
			value:      "=value",
			operator:   EQUAL,
			err:        nil,
		}, {
			expression: "key=~=value",
			key:        "key",
			value:      "=value",
			operator:   MATCH,
			err:        nil,
		}, {
			expression: "key",
			err:        errInvalidQuery,
		},
	}

	for _, tc := range testCases {
		expression, err := parseExpression(tc.expression)
		if err != tc.err || (err == nil && (expression.key != tc.key || expression.value != tc.value || expression.operator != tc.operator)) {
			t.Fatalf("Expected the values %s, %s, %d, %q, but got %s, %s, %d, %q", tc.key, tc.value, tc.operator, tc.err, expression.key, expression.value, expression.operator, err)
		}
	}
}

func BenchmarkExpressionParsing(b *testing.B) {
	expressions := [][]string{
		{"key=value", "key!=value"},
		{"key=~value", "key!=~value"},
		{"key1=~", "key2=~"},
		{"key1!=~aaa", "key2=~abc"},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for i := 0; i < 4; i++ {
			NewTagQuery(expressions[i], 0)
		}
	}
}
