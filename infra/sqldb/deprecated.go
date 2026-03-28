package sqldb

// Deprecated: Use [Config] instead.
type MySQLConfig = Config

// Deprecated: Use [Config] instead.
type PostgresConfig = Config

// Deprecated: Use [Fields] instead.
type MySQLFields = Fields

// Deprecated: Use [Fields] instead.
type PostgresFields = Fields

// Deprecated: Use [LoadFields] with appropriate driver defaults instead.
// Example: LoadFields(envPrefix, 3306, "mysql", maxIdle, maxOpen)
func LoadMySQLFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	return LoadFields(envPrefix, 3306, "mysql", defaultMaxIdle, defaultMaxOpen)
}

// Deprecated: Use [LoadFields] with appropriate driver defaults instead.
// Example: LoadFields(envPrefix, 5432, "postgres", maxIdle, maxOpen)
func LoadPostgresFields(envPrefix string, defaultMaxIdle, defaultMaxOpen int) (Fields, error) {
	return LoadFields(envPrefix, 5432, "postgres", defaultMaxIdle, defaultMaxOpen)
}

// Deprecated: Use [Fields.Validate] with driver "mysql" instead.
// Example: f.Validate(envPrefix, environment, "mysql")
func (f Fields) ValidateMySQL(envPrefix, environment string) error {
	return f.Validate(envPrefix, environment, "mysql")
}

// Deprecated: Use [Fields.Validate] with driver "postgres" instead.
// Example: f.Validate(envPrefix, environment, "postgres")
func (f Fields) ValidatePostgres(envPrefix, environment string) error {
	return f.Validate(envPrefix, environment, "postgres")
}
