package store

// PGEnvForTest exposes the environment built for pg_dump and pg_restore.
func PGEnvForTest(databaseURL string) ([]string, error) {
	env, err := pgEnv(databaseURL)
	return env.vars, err
}

// SetsMaxConnsForTest exposes whether a connection string sets pool_max_conns.
func SetsMaxConnsForTest(databaseURL string) bool { return setsMaxConns(databaseURL) }
