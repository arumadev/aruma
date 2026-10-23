// Lists, sets, maps and map diffs.
const DOC = 'DOC';

export const topic = 'collections';
export const service = 'firestore';
export const resource = DOC;

const x = (id, expr, more = {}) => ({ id, desc: expr, expr, ...more });
const OLD = { a: 1, b: 2, m: { x: 1 }, keep: 'k' };
const NEW = { a: 1, b: 3, m: { x: 2 }, keep: 'k', c: 4 };

export const cases = [
  x('list_size', '[1, 2, 3].size() == 3'),
  x('list_in', '1 in [1, 2]'),
  x('list_hasall', '[1, 2].hasAll([1])'),
  x('list_hasall_empty', '[1, 2].hasAll([])'),
  x('list_hasany_false', '[1, 2].hasAny([3])'),
  x('list_hasany_empty', '[].hasAny([])'),
  x('list_hasonly', '[1, 2].hasOnly([1, 2, 3])'),
  x('list_hasonly_dupes', '[1, 1].hasOnly([1])'),
  x('list_concat', '[1, 2].concat([3]) == [1, 2, 3]'),
  x('list_plus', '[1, 2] + [3] == [1, 2, 3]'),
  x('list_removeall', '[1, 2, 1].removeAll([1]) == [2]'),
  x('list_index', '[1, 2, 3][1] == 2'),
  x('list_index_out_of_range', '[1, 2, 3][5] == 1'),
  x('list_index_negative', '[1, 2, 3][-1] == 3'),
  x('list_index_float', '[1, 2, 3][1.0] == 2'),
  x('list_range', '[1, 2, 3][0:2] == [1, 2]'),
  x('list_range_past_end', '[1, 2, 3][1:9] == [2, 3]'),
  x('list_join', "['a', 'b'].join(',') == 'a,b'"),
  x('list_join_ints', "[1, 2].join(',') == '1,2'"),
  x('list_order_matters', '[1, 2] == [2, 1]'),
  x('list_mixed_types', "[1, 'a', null].size() == 3"),
  x('list_neg_zero', '[0.0] == [-0.0]'),
  x('list_hasall_in_float', '[1].hasAll([1.0])'),
  x('list_in_map', "{'a': 1} in [{'a': 1}]"),
  x('list_tostring_compare', "['b', 'a'] < ['c']"),
  x('set_eq', '[1, 2, 2].toSet() == [2, 1].toSet()'),
  x('set_size', '[1, 1, 2].toSet().size() == 2'),
  x('set_in', '1 in [1].toSet()'),
  x('set_in_float', '1 in [1.0].toSet()'),
  x('set_int_float_dedupe', '[1, 1.0].toSet().size() == 1'),
  x('set_difference', '[1, 2].toSet().difference([2].toSet()) == [1].toSet()'),
  x('set_union', '[1].toSet().union([2].toSet()) == [1, 2].toSet()'),
  x('set_intersection', '[1, 2].toSet().intersection([2, 3].toSet()) == [2].toSet()'),
  x('set_hasall_list', '[1, 2].toSet().hasAll([1])'),
  x('set_hasonly', '[1, 2].toSet().hasOnly([1, 2, 3])'),
  x('set_difference_list_arg', '[1, 2].toSet().difference([2]) == [1].toSet()'),
  x('set_eq_list', '[1].toSet() == [1]'),
  x('set_index', '[1].toSet()[0] == 1'),
  x('map_keys', "{'a': 1}.keys() == ['a']"),
  x('map_values', "{'a': 1}.values() == [1]"),
  x('map_keys_order_sorted', "{'b': 1, 'a': 2}.keys() == ['a', 'b']"),
  x('map_keys_order_insertion', "{'b': 1, 'a': 2}.keys() == ['b', 'a']"),
  x('map_values_order', "{'b': 1, 'a': 2}.values() == [2, 1]"),
  x('map_size', "{'a': 1, 'b': 2}.size() == 2"),
  x('map_in_key', "'a' in {'a': 1}"),
  x('map_in_value', "1 in {'a': 1}"),
  x('map_get', "{'a': 1}.get('a', 0) == 1"),
  x('map_get_default', "{'a': 1}.get('b', 0) == 0"),
  x('map_get_path', "{'a': {'b': 1}}.get(['a', 'b'], 0) == 1"),
  x('map_get_path_through_int', "{'a': 1}.get(['a', 'b'], 0) == 0"),
  x('map_get_empty_path', "{'a': 1}.get([], 0) == 0"),
  x('map_get_int_key', "{'a': 1}.get(1, 0) == 0"),
  x('map_dot', "{'a': 1}.a == 1"),
  x('map_index', "{'a': 1}['a'] == 1"),
  x('map_missing', "{'a': 1}.b == 1"),
  x('map_duplicate_key', "{'a': 1, 'a': 2}.a == 2", { alone: true }),
  x('map_int_key', '{1: 2}.size() == 1', { alone: true }),
  x('map_eq', "{'a': 1, 'b': 2} == {'b': 2, 'a': 1}"),
  x('map_hasall_direct', "{'a': 1}.hasAll(['a'])"),
  x('doc_keys_hasall', "resource.data.keys().hasAll(['n', 'f'])"),
  x('doc_keys_hasonly', "resource.data.keys().hasOnly(['n', 'f', 's', 'b', 'nul', 'arr', 'tags', 'm', 'owner'])"),
  x('diff_affected', "{'a': 1, 'b': 2}.diff({'a': 1, 'b': 3, 'c': 4}).affectedKeys() == ['b', 'c'].toSet()"),
  x('diff_added', "{'a': 1}.diff({'a': 1, 'c': 4}).addedKeys() == ['c'].toSet()"),
  x('diff_removed', "{'a': 1, 'c': 4}.diff({'a': 1}).removedKeys() == ['c'].toSet()"),
  x('diff_changed', "{'a': 1}.diff({'a': 2}).changedKeys() == ['a'].toSet()"),
  x('diff_unchanged', "{'a': 1, 'b': 2}.diff({'a': 1, 'b': 3}).unchangedKeys() == ['a'].toSet()"),
  x('diff_int_float_changed', "{'a': 1}.diff({'a': 1.0}).changedKeys().size() == 0"),
  x('diff_nested', "{'m': {'x': 1}}.diff({'m': {'x': 2}}).affectedKeys() == ['m'].toSet()"),
  x('diff_not_map', "{'a': 1}.diff([1]).affectedKeys().size() == 0"),
  x('diff_on_update', "request.resource.data.diff(resource.data).affectedKeys() == ['b', 'c', 'm'].toSet()", {
    method: 'update',
    resource: OLD,
    data: NEW,
  }),
  x('diff_on_update_hasonly', "request.resource.data.diff(resource.data).affectedKeys().hasOnly(['b', 'c', 'm'])", {
    method: 'update',
    resource: OLD,
    data: NEW,
  }),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set([
  'list_neg_zero', 'set_in_float', 'map_keys_order_sorted', 'map_keys_order_insertion', 'list_plus', 'list_index_negative',
  'map_get_path_through_int', 'diff_int_float_changed', 'diff_on_update', 'list_join',
]);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
