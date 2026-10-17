package store

// PGEnvForTest exposes the environment built for pg_dump and pg_restore.
func PGEnvForTest(databaseURL string) ([]string, error) {
	env, err := pgEnv(databaseURL)
	return env.vars, err
}
