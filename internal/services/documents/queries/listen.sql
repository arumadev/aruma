-- The hub's reads (ADR-024 §4, ADR-026 §2): the commits it missed, from the changelog, and the
-- changes a resumed target catches up on (core/listenread.go).

-- name: LoggedCommits :many
-- The changelog rows of the commits after @after_seq, up to @max_seq and to the sequencer's seq
-- in the same snapshot, in seq then name_key order: every row returned is committed, and the
-- commits are consecutive from the first, whole. The head comes on every row, and alone (with
-- NULL columns) when there is none. A first seq past @after_seq + 1 under a head at or past it
-- means the changelog lost the rows between to retention.
SELECT s.seq AS head, s.last_ts AS head_time, c.seq, c.commit_time, c.name, c.before, c.after
FROM documents.sequencer s
LEFT JOIN documents.changelog c ON c.project_number = s.project_number AND c.database_id = s.database_id
    AND c.seq > @after_seq AND c.seq <= s.seq AND c.seq <= @max_seq
WHERE s.project_number = @project_number AND s.database_id = @database_id
ORDER BY c.seq, c.name_key;

-- name: ChangesWindow :one
-- Whether the changes after @since are all still held: @since is within the changelog's
-- retention (@retention_secs, which no pass prunes younger) and the database's read_time window;
-- and the first commit after @since, 0 for none. The changelog is read in seq order from its
-- oldest row, a prefix older than @since at most.
SELECT (@since::timestamptz > now() - make_interval(secs => @retention_secs::float8)
        AND @since::timestamptz >= now() - b.version_retention)::boolean AS in_window,
    coalesce((SELECT c.seq FROM documents.changelog c
        WHERE c.project_number = @project_number AND c.database_id = @database_id AND c.commit_time > @since
        ORDER BY c.seq LIMIT 1), 0)::bigint AS first_seq
FROM documents.databases b
WHERE b.project_number = @project_number AND b.database_id = @database_id;

-- name: ChangesBetween :many
-- The documents of a scope that the commits after @after_seq, up to @upto_seq, changed, each once
-- in name_key order: its image before the first of those commits and after the last (NULL when
-- it did not exist), with the last one's commit_time, the image's update_time. The scope is ChangedSince's (indexes.sql), filtered alike.
SELECT DISTINCT ON (c.name_key) c.name,
    (first_value(c.before) OVER (PARTITION BY c.name_key ORDER BY c.seq
        ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING))::bytea AS before,
    (last_value(c.after) OVER (PARTITION BY c.name_key ORDER BY c.seq
        ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING))::bytea AS after,
    (last_value(c.commit_time) OVER (PARTITION BY c.name_key ORDER BY c.seq
        ROWS BETWEEN UNBOUNDED PRECEDING AND UNBOUNDED FOLLOWING))::timestamptz AS after_time
FROM documents.changelog c, LATERAL (SELECT reverse(c.name) AS r) x
WHERE c.project_number = @project_number AND c.database_id = @database_id
    AND c.seq > @after_seq AND c.seq <= @upto_seq
    AND c.name >= @lo AND c.name < @hi
    AND (@in_group::boolean
            AND (substr(x.r, 10, octet_length(@group_numeric::bytea)) = @group_numeric::bytea
                OR substr(x.r, 1, 3) = '\x000100'::bytea
                    AND substr(x.r, position('\x0100'::bytea IN substr(x.r, 4)) + 2,
                        octet_length(@group_text::bytea)) = @group_text::bytea)
        OR NOT @in_group::boolean
            AND (octet_length(c.name) - @prefix_len::int = 10
                OR substr(x.r, 1, 3) = '\x000100'::bytea
                    AND position('\x0100'::bytea IN substr(x.r, 4)) + 2 = octet_length(c.name) - @prefix_len::int))
ORDER BY c.name_key, c.seq;
