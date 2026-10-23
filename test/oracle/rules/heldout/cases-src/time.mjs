// Timestamps and durations: accessors, constructors, arithmetic, units,
// comparisons with other types.
const DOC = 'DOC';

export const topic = 'time';
export const service = 'firestore';
export const resource = DOC;

// 2026-03-04 is a Wednesday, day 63 of the year.
const T = '2026-03-04T05:06:07.123456Z';
const x = (id, expr, more = {}) => ({ id, desc: expr, expr, time: T, ...more });

export const cases = [
  x('year', 'request.time.year() == 2026'),
  x('month', 'request.time.month() == 3'),
  x('day', 'request.time.day() == 4'),
  x('hours', 'request.time.hours() == 5'),
  x('minutes', 'request.time.minutes() == 6'),
  x('seconds', 'request.time.seconds() == 7'),
  x('nanos', 'request.time.nanos() == 123456000'),
  x('day_of_week_wed_3', 'request.time.dayOfWeek() == 3'),
  x('day_of_week_wed_4', 'request.time.dayOfWeek() == 4'),
  x('day_of_year', 'request.time.dayOfYear() == 63'),
  x('to_millis', 'request.time.toMillis() == 1772600767123'),
  x('date', 'request.time.date() == timestamp.date(2026, 3, 4)'),
  x('time_of_day', 'request.time.time() == duration.time(5, 6, 7, 123456000)'),
  x('ts_date_type', 'timestamp.date(2026, 3, 4) is timestamp'),
  x('ts_value_millis', 'timestamp.value(1772600767123) == timestamp.date(2026, 3, 4) + duration.time(5, 6, 7, 123000000)'),
  x('ts_date_feb_30', 'timestamp.date(2026, 2, 30) is timestamp'),
  x('ts_date_month_13', 'timestamp.date(2026, 13, 1) is timestamp'),
  x('ts_date_year_0', 'timestamp.date(0, 1, 1) is timestamp'),
  x('ts_date_year_10000', 'timestamp.date(10000, 1, 1) is timestamp'),
  x('ts_plus_duration', 'request.time + duration.value(1, \'d\') > request.time'),
  x('duration_plus_ts', 'duration.value(1, \'d\') + request.time > request.time'),
  x('ts_minus_ts', 'request.time - request.time == duration.value(0, \'s\')'),
  x('ts_minus_ts_seconds', '(request.time - timestamp.date(2026, 3, 4)).seconds() == 18367'),
  x('ts_minus_duration', 'timestamp.date(2026, 3, 4) - duration.value(1, \'d\') == timestamp.date(2026, 3, 3)'),
  x('ts_plus_int', 'request.time + 1 > request.time'),
  x('ts_gt_int', 'request.time > 1'),
  x('ts_eq_string', "request.time == '2026-03-04T05:06:07.123456Z'"),
  x('ts_lt_ts', 'timestamp.date(2026, 3, 3) < request.time'),
  x('units_w', "duration.value(1, 'w') == duration.value(7, 'd')"),
  x('units_h_m', "duration.value(1, 'h') == duration.value(60, 'm')"),
  x('units_s_ms', "duration.value(1, 's') == duration.value(1000, 'ms')"),
  x('units_ns', "duration.value(1, 'ms') == duration.value(1000000, 'ns')"),
  x('units_us_absent', "duration.value(1, 'us') == duration.value(1000, 'ns')"),
  x('unit_unknown', "duration.value(1, 'x') is duration"),
  x('unit_float_amount', "duration.value(1.5, 's') is duration"),
  x('duration_seconds', "duration.value(90, 'm').seconds() == 5400"),
  x('duration_nanos_part', "duration.value(1500, 'ms').nanos() == 500000000"),
  x('duration_negative_parts', "duration.value(-1500, 'ms').seconds() == -1 && duration.value(-1500, 'ms').nanos() == -500000000"),
  x('duration_abs', "duration.abs(duration.value(-5, 's')) == duration.value(5, 's')"),
  x('duration_time', 'duration.time(1, 2, 3, 4).nanos() == 4 && duration.time(1, 2, 3, 4).seconds() == 3723'),
  x('duration_compare', "duration.value(1, 'm') > duration.value(59, 's')"),
  x('duration_times_int', "duration.value(1, 's') * 2 == duration.value(2, 's')"),
  x('duration_eq_int', "duration.value(0, 's') == 0"),
  x('ts_round_trip_offset', 'request.time == timestamp.value(request.time.toMillis())'),
];

// Cited by docs/research/rules/A-oracle-findings.md: kept in the development set.
const PIN = new Set(['day_of_week_wed_3', 'day_of_week_wed_4', 'ts_date_feb_30', 'units_us_absent', 'ts_plus_int', 'duration_nanos_part']);
for (const c of cases) if (PIN.has(c.id)) c.pin = true;
