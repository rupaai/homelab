package core

// What database: in homelab.yaml gives an app environment.
const (
	DatabasePostgres = "postgres"
	DatabaseSQLite   = "sqlite"

	// DatabaseService is the postgres container's name; the app reaches it as "db".
	DatabaseService = "db"
	// DatabaseImage is the postgres image, pinned to a major version.
	DatabaseImage = "postgres:17-alpine"
	// DatabaseMemory is the postgres container's memory limit.
	DatabaseMemory = "256m"
	// DatabaseUser and DatabaseName are both "app".
	DatabaseUser = "app"
	DatabaseName = "app"
	// DatabaseFolder is the data folder's name, for postgres's files and for sqlite's file.
	DatabaseFolder = "homelab-db"
	// DatabaseSQLiteDir is where the app sees the sqlite folder; the file in it is app.db.
	DatabaseSQLiteDir  = "/homelab/db"
	DatabaseSQLiteFile = "app.db"
	// DatabasePostgresDir is where postgres keeps its files in its container.
	DatabasePostgresDir = "/var/lib/postgresql/data"

	// DatabasePassword is the secret Homelab makes for the postgres user. DatabaseURL (a
	// secret for postgres, a setting for sqlite) and DatabasePath are what the app reads.
	DatabasePassword = "DATABASE_PASSWORD"
	DatabaseURL      = "DATABASE_URL"
	DatabasePath     = "DATABASE_PATH"
)

// DatabaseNames are the setting and secret names Homelab sets for an app's database, so
// the app's own homelab.yaml may not use them.
func DatabaseNames(engine string) []string {
	if engine == DatabasePostgres {
		return []string{DatabasePassword, DatabaseURL}
	}
	return []string{DatabaseURL, DatabasePath}
}

// DatabaseRegistration tells homelab-services where an environment's database is, so it
// can look inside it.
type DatabaseRegistration struct {
	Engine string `json:"engine"`
	Port   int    `json:"port,omitempty"` // postgres: published on 127.0.0.1
	Path   string `json:"path,omitempty"` // sqlite: the file on this computer
}
