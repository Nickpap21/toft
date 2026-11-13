// Package catalog is a static registry of pre-configured addon templates.
// Zero DB reads — all data lives in-process.
package catalog

import (
	"embed"
	"strings"
)

// Template kinds: one image, or a compose stack (D-147).
const (
	KindImage   = "image"
	KindCompose = "compose"
)

// The compose templates' files, one per slug.
//
//go:embed compose/*.yml
var composeFiles embed.FS

func composeFile(slug string) string {
	b, err := composeFiles.ReadFile("compose/" + slug + ".yml")
	if err != nil {
		panic("catalog: no compose file for " + slug) // a build-time mistake: the test loads every one
	}
	return string(b)
}

// EnvVarSpec describes one environment variable required by an addon template.
type EnvVarSpec struct {
	Key         string `json:"key"`
	Description string `json:"description"`
	Default     string `json:"default"`
	IsSecret    bool   `json:"is_secret"`
	// Generate is how a secret's value is made when the create doesn't send
	// it (the browser's, or service.Create's): "" a 24-character password,
	// "long" 64 characters, "hex64" 32 random bytes in hex.
	Generate string `json:"generate,omitempty"`
}

// VolumeMount describes a named Docker volume for an addon.
type VolumeMount struct {
	Name   string `json:"name"`   // volume name suffix appended to "paas-vol-{id[:8]}-"
	Target string `json:"target"` // container mount path
}

// ConfigFile describes a default config file to seed when creating an addon service.
type ConfigFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Template describes a pre-configured addon that can be deployed as a service.
type Template struct {
	Slug          string        `json:"slug"`
	Kind          string        `json:"kind"`
	Name          string        `json:"name"`
	Category      string        `json:"category"` // database|cache|messaging|tools|storage|observability
	Description   string        `json:"description"`
	DockerImage   string        `json:"docker_image"`
	DefaultPort   int           `json:"default_port"`
	SecondaryPort int           `json:"secondary_port,omitempty"` // 0 = none
	NetworkAlias  string        `json:"network_alias"`
	StartCommand  string        `json:"start_command,omitempty"`
	Volumes       []VolumeMount `json:"volumes"`
	EnvVarSpecs   []EnvVarSpec  `json:"env_var_specs"`
	ConfigFiles   []ConfigFile  `json:"config_files,omitempty"`
	// IsObservability marks templates that are part of the PLG observability stack.
	// When deployed, the deployment service automatically registers them in observability_configs.
	IsObservability bool `json:"is_observability,omitempty"`
	// A compose template's file (its variables as ${VAR}), the compose
	// service the domain goes to on DefaultPort, and how many services it
	// runs. DockerImage and NetworkAlias are empty.
	ComposeContent  string `json:"compose_content,omitempty"`
	RouteService    string `json:"route_service,omitempty"`
	ComposeServices int    `json:"compose_services,omitempty"`
	// SetupLogin: whoever opens the app first becomes its admin, and no
	// variable sets that account (or setting it locks it). A create with no
	// basic auth gets a generated one in front of it until then.
	SetupLogin bool `json:"setup_login,omitempty"`
	// AdminPassword is the secret spec holding its admin (or root) login's
	// password: kept for the service's creator until dismissed (D-160).
	AdminPassword string `json:"admin_password,omitempty"`
	// AdminUser is the variable holding that login's user, shown beside the
	// password; AdminUserFixed is the user when no variable sets it.
	AdminUser      string `json:"-"`
	AdminUserFixed string `json:"-"`
	// AdminReset is how Reset gives that login a new password (D-160); nil
	// for none (a database root: other services connect with it).
	AdminReset *AdminReset `json:"-"`
}

// AdminReset runs Argv in the service's running container (its compose
// service Container, "" for its own), with Stdin(password) on its standard
// input: the password never travels in a command line. Argv nil: the app
// reads the secret at each start, so a deploy applies it (MinIO).
type AdminReset struct {
	Container string
	Argv      func(user string) []string
	Env       []string
	WorkDir   string
	Stdin     func(password string) string
	// Prints, when set, is the command's whole output on success; anything
	// else fails it, as an exit code would.
	Prints string
}

// line is a password on a line of its own.
func line(password string) string { return password + "\n" }

// TemporaryLoginUser is the basic auth user Toft puts in front of a
// SetupLogin app; Needs attention asks to turn off only that one.
const TemporaryLoginUser = "toft"

// All returns the full addon catalog.
func All() []Template { return templates }

// withKinds marks the templates that leave Kind empty as single images.
func withKinds(ts []Template) []Template {
	for i := range ts {
		if ts[i].Kind == "" {
			ts[i].Kind = KindImage
		}
	}
	return ts
}

// Get returns the template for the given slug, or false if not found. A
// retired template (not in All) is found too, for the services made from it.
func Get(slug string) (Template, bool) {
	for _, t := range templates {
		if t.Slug == slug {
			return t, true
		}
	}
	for _, t := range retired {
		if t.Slug == slug {
			return t, true
		}
	}
	return Template{}, false
}

// retired are templates no longer offered whose services still run: Get
// finds them (their connect-to port), All and the image check don't.
var retired = withKinds([]Template{
	{
		// Replaced by Mailpit: its image is amd64-only and unmaintained.
		Slug:         "mailhog",
		Name:         "MailHog",
		Category:     "tools",
		Description:  "Web and API based SMTP testing tool. Catches all outgoing email.",
		DockerImage:  "mailhog/mailhog",
		DefaultPort:  8025,
		NetworkAlias: "mailhog",
		Volumes:      []VolumeMount{},
		EnvVarSpecs:  []EnvVarSpec{},
	},
})

var templates = withKinds([]Template{
	{
		Slug:         "postgres",
		Name:         "PostgreSQL",
		Category:     "database",
		Description:  "The world's most advanced open-source relational database.",
		DockerImage:  "postgres:16",
		DefaultPort:  5432,
		NetworkAlias: "postgres",
		Volumes:      []VolumeMount{{Name: "pg-data", Target: "/var/lib/postgresql/data"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "POSTGRES_USER", Description: "Superuser name", Default: "postgres"},
			{Key: "POSTGRES_PASSWORD", Description: "Superuser password", Default: "changeme", IsSecret: true},
			{Key: "POSTGRES_DB", Description: "Default database name", Default: "app"},
		},
		AdminPassword: "POSTGRES_PASSWORD",
		AdminUser:     "POSTGRES_USER",
	},
	{
		Slug:         "mysql",
		Name:         "MySQL",
		Category:     "database",
		Description:  "The most popular open-source relational database.",
		DockerImage:  "mysql:8",
		DefaultPort:  3306,
		NetworkAlias: "mysql",
		Volumes:      []VolumeMount{{Name: "mysql-data", Target: "/var/lib/mysql"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "MYSQL_ROOT_PASSWORD", Description: "Root password", Default: "changeme", IsSecret: true},
			{Key: "MYSQL_DATABASE", Description: "Database to create on startup", Default: "app"},
			{Key: "MYSQL_USER", Description: "Additional user", Default: "app"},
			{Key: "MYSQL_PASSWORD", Description: "Additional user password", Default: "changeme", IsSecret: true},
		},
		AdminPassword:  "MYSQL_ROOT_PASSWORD",
		AdminUserFixed: "root",
	},
	{
		Slug:         "redis",
		Name:         "Redis",
		Category:     "cache",
		Description:  "In-memory data structure store used as cache, message broker, and streaming engine.",
		DockerImage:  "redis:7-alpine",
		DefaultPort:  6379,
		NetworkAlias: "redis",
		Volumes:      []VolumeMount{{Name: "redis-data", Target: "/data"}},
		EnvVarSpecs:  []EnvVarSpec{},
	},
	{
		Slug:         "mongodb",
		Name:         "MongoDB",
		Category:     "database",
		Description:  "The leading NoSQL document database.",
		DockerImage:  "mongo:7",
		DefaultPort:  27017,
		NetworkAlias: "mongodb",
		Volumes:      []VolumeMount{{Name: "mongo-data", Target: "/data/db"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "MONGO_INITDB_ROOT_USERNAME", Description: "Root username", Default: "root"},
			{Key: "MONGO_INITDB_ROOT_PASSWORD", Description: "Root password", Default: "changeme", IsSecret: true},
		},
		AdminPassword: "MONGO_INITDB_ROOT_PASSWORD",
		AdminUser:     "MONGO_INITDB_ROOT_USERNAME",
	},
	{
		Slug:         "pgadmin",
		Name:         "pgAdmin",
		Category:     "tools",
		Description:  "Web-based administration tool for PostgreSQL.",
		DockerImage:  "dpage/pgadmin4:9.19.0@sha256:3591b3d02910dcd817dec6fcaa681c278ba6966945283dd0ea0042731c330c83",
		DefaultPort:  8080,
		NetworkAlias: "pgadmin",
		Volumes:      []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "PGADMIN_DEFAULT_EMAIL", Description: "Admin login email", Default: "admin@example.com"},
			{Key: "PGADMIN_DEFAULT_PASSWORD", Description: "Admin login password", Default: "changeme", IsSecret: true},
			// pgAdmin runs as a non-root user and binds 80 through a file
			// capability, which no-new-privileges (D-93) strips; newer
			// images then move to 8080 on their own, older ones fail to
			// bind. Asking for 8080 works on both.
			{Key: "PGADMIN_LISTEN_PORT", Description: "The port pgAdmin listens on: the service's port", Default: "8080"},
		},
		AdminPassword: "PGADMIN_DEFAULT_PASSWORD",
		AdminUser:     "PGADMIN_DEFAULT_EMAIL",
		// setup.py takes the password only as --password: Python reads it from
		// stdin and hands it to setup.py inside the process, off its command line.
		AdminReset: &AdminReset{
			Argv: func(user string) []string {
				return []string{"/venv/bin/python3", "-c", "import runpy, sys; pw = sys.stdin.readline().rstrip('\\n'); sys.argv = ['setup.py', 'update-user', sys.argv[1], '--password', pw]; runpy.run_path('setup.py', run_name='__main__')", user}
			},
			WorkDir: "/pgadmin4",
			Stdin:   line,
		},
	},
	{
		Slug:         "phpmyadmin",
		Name:         "phpMyAdmin",
		Category:     "tools",
		Description:  "Web interface for MySQL administration.",
		DockerImage:  "phpmyadmin",
		DefaultPort:  80,
		NetworkAlias: "phpmyadmin",
		Volumes:      []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "PMA_HOST", Description: "MySQL host (use network alias)", Default: "mysql"},
		},
	},
	{
		// MailHog's successor: the same ports (web 8025, SMTP 1025), on
		// amd64 and arm64. Mail is kept in a temporary file, as MailHog kept
		// it in memory.
		Slug:         "mailpit",
		Name:         "Mailpit",
		Category:     "tools",
		Description:  "Web and API based SMTP testing tool. Catches all outgoing email on port 1025.",
		DockerImage:  "axllent/mailpit:v1.33.0@sha256:9b3a463be15354c0f0eba91697d80e3e0d8a5623ef0e627cacc836bc17fd5231",
		DefaultPort:  8025,
		NetworkAlias: "mailpit",
		Volumes:      []VolumeMount{},
		EnvVarSpecs:  []EnvVarSpec{},
	},
	{
		Slug:        "minio",
		Name:        "MinIO",
		Category:    "storage",
		Description: "High-performance S3-compatible object storage.",
		// MinIO stopped publishing images (minio/minio is gone from Docker
		// Hub, quay.io's needs an account); PGSTY Silo is its maintained
		// fork, with the same MINIO_* variables, command and data.
		DockerImage:   "pgsty/silo:RELEASE.2026-10-26T00-00-00Z",
		DefaultPort:   9000,
		SecondaryPort: 9001,
		NetworkAlias:  "minio",
		StartCommand:  "server /data --console-address :9001",
		Volumes:       []VolumeMount{{Name: "minio-data", Target: "/data"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "MINIO_ROOT_USER", Description: "Root access key", Default: "minioadmin"},
			{Key: "MINIO_ROOT_PASSWORD", Description: "Root secret key", Default: "changeme", IsSecret: true},
		},
		AdminPassword: "MINIO_ROOT_PASSWORD",
		AdminUser:     "MINIO_ROOT_USER",
		AdminReset:    &AdminReset{},
	},
	{
		// Per app, never a shared workspace service (D-59): it keeps its
		// index in memory, and its scoped keys separate collections, not
		// tenants.
		Slug:         "typesense",
		Name:         "Typesense",
		Category:     "database",
		Description:  "Typo-tolerant search engine, with its index on a volume.",
		DockerImage:  "typesense/typesense:30.2",
		DefaultPort:  8108,
		NetworkAlias: "typesense",
		Volumes:      []VolumeMount{{Name: "typesense-data", Target: "/data"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "TYPESENSE_API_KEY", Description: "Admin API key; apps send it as X-TYPESENSE-API-KEY", IsSecret: true},
			{Key: "TYPESENSE_DATA_DIR", Description: "Where the index lives", Default: "/data"},
		},
		AdminPassword: "TYPESENSE_API_KEY",
	},
	{
		Slug:         "redisinsight",
		Name:         "RedisInsight",
		Category:     "tools",
		Description:  "Official Redis GUI — browser-based tool for visualising data, running commands, and profiling Redis.",
		DockerImage:  "redis/redisinsight:latest",
		DefaultPort:  5540,
		NetworkAlias: "redisinsight",
		Volumes:      []VolumeMount{{Name: "redisinsight-data", Target: "/data"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "RI_ENCRYPTION_KEY", Description: "Passphrase used to encrypt stored Redis connection credentials on disk. Generate with: openssl rand -base64 32", Default: "", IsSecret: true},
		},
	},
	{
		Slug:         "adminer",
		Name:         "Adminer",
		Category:     "tools",
		Description:  "Lightweight database management tool supporting MySQL, PostgreSQL, SQLite and more.",
		DockerImage:  "adminer",
		DefaultPort:  8080,
		NetworkAlias: "adminer",
		Volumes:      []VolumeMount{},
		EnvVarSpecs:  []EnvVarSpec{},
	},
	{
		Slug:          "rabbitmq",
		Name:          "RabbitMQ",
		Category:      "messaging",
		Description:   "Reliable open-source message broker with management UI.",
		DockerImage:   "rabbitmq:3-management",
		DefaultPort:   5672,
		SecondaryPort: 15672,
		NetworkAlias:  "rabbitmq",
		Volumes:       []VolumeMount{{Name: "rabbit-data", Target: "/var/lib/rabbitmq"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "RABBITMQ_DEFAULT_USER", Description: "Default username", Default: "guest"},
			{Key: "RABBITMQ_DEFAULT_PASS", Description: "Default password", Default: "guest", IsSecret: true},
		},
		AdminPassword: "RABBITMQ_DEFAULT_PASS",
		AdminUser:     "RABBITMQ_DEFAULT_USER",
		AdminReset: &AdminReset{
			Argv:  func(user string) []string { return []string{"rabbitmqctl", "change_password", user} },
			Stdin: line,
		},
	},
	// ── Web / static file serving ─────────────────────────────────────────────
	{
		Slug:         "nginx",
		Name:         "Nginx",
		Category:     "tools",
		Description:  "High-performance web server and reverse proxy. Use to serve static files from a named volume or a host bind-mount.",
		DockerImage:  "nginx:alpine",
		DefaultPort:  80,
		NetworkAlias: "nginx",
		Volumes:      []VolumeMount{{Name: "nginx-html", Target: "/usr/share/nginx/html"}},
		EnvVarSpecs:  []EnvVarSpec{},
		ConfigFiles: []ConfigFile{{
			Path: "/etc/nginx/conf.d/default.conf",
			Content: `server {
    listen 80;
    server_name _;
    root /usr/share/nginx/html;
    index index.html index.htm;

    location / {
        try_files $uri $uri/ =404;
    }
}`,
		}},
	},
	// ── Observability stack (PLG) ──────────────────────────────────────────────
	{
		Slug:            "loki",
		Name:            "Loki",
		Category:        "observability",
		Description:     "Horizontally-scalable log aggregation system by Grafana Labs. Zero-instrumentation: uses the Docker log driver.",
		DockerImage:     "grafana/loki:3.0.0",
		DefaultPort:     3100,
		NetworkAlias:    "loki",
		Volumes:         []VolumeMount{{Name: "loki-data", Target: "/loki"}},
		EnvVarSpecs:     []EnvVarSpec{},
		IsObservability: true,
	},
	{
		Slug:            "prometheus",
		Name:            "Prometheus",
		Category:        "observability",
		Description:     "Open-source monitoring and alerting toolkit. Scrapes metrics from instrumented services.",
		DockerImage:     "prom/prometheus:v2.52.0",
		DefaultPort:     9090,
		NetworkAlias:    "prometheus",
		Volumes:         []VolumeMount{{Name: "prom-data", Target: "/prometheus"}},
		EnvVarSpecs:     []EnvVarSpec{},
		IsObservability: true,
	},
	{
		Slug:         "grafana",
		Name:         "Grafana",
		Category:     "observability",
		Description:  "Visualisation and analytics platform. Connects to Loki and Prometheus for unified dashboards.",
		DockerImage:  "grafana/grafana:10.4.2",
		DefaultPort:  3001,
		NetworkAlias: "grafana",
		Volumes:      []VolumeMount{{Name: "grafana-data", Target: "/var/lib/grafana"}},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "GF_SECURITY_ADMIN_USER", Description: "Grafana admin username", Default: "admin"},
			{Key: "GF_SECURITY_ADMIN_PASSWORD", Description: "Grafana admin password", Default: "changeme", IsSecret: true},
			// Grafana listens on 3000 unless told otherwise; the service's port is 3001.
			{Key: "GF_SERVER_HTTP_PORT", Description: "The port Grafana listens on: the service's port", Default: "3001"},
		},
		// Loki and Prometheus as data sources, by their aliases, which resolve
		// between containers on the same server (D-48). Grafana reads no
		// variable for this, so it is a provisioning file.
		ConfigFiles: []ConfigFile{{
			Path: "/etc/grafana/provisioning/datasources/toft.yaml",
			Content: `apiVersion: 1
datasources:
  - name: Loki
    type: loki
    access: proxy
    url: http://loki:3100
  - name: Prometheus
    type: prometheus
    access: proxy
    url: http://prometheus:9090
    isDefault: true
`,
		}},
		IsObservability: true,
		AdminPassword:   "GF_SECURITY_ADMIN_PASSWORD",
		AdminUser:       "GF_SECURITY_ADMIN_USER",
		AdminReset: &AdminReset{
			// User 1 is the admin GF_SECURITY_ADMIN_USER named at the first start.
			Argv: func(string) []string {
				return []string{"grafana", "cli", "admin", "reset-admin-password", "--password-from-stdin"}
			},
			Stdin: line,
		},
	},
	// Apps: compose stacks (D-147). Their own address reaches them as
	// TOFT_URL, set at each deploy from the service's first domain.
	{
		Kind: KindCompose, Slug: "uptime-kuma", Name: "Uptime Kuma", Category: "apps",
		Description:    "Uptime monitoring with status pages. Docker monitoring is off: Toft's deploy policy refuses the Docker socket.",
		ComposeContent: composeFile("uptime-kuma"), RouteService: "uptime-kuma", DefaultPort: 3001, ComposeServices: 1, SetupLogin: true,
		Volumes: []VolumeMount{}, EnvVarSpecs: []EnvVarSpec{},
	},
	{
		Kind: KindCompose, Slug: "plausible", Name: "Plausible", Category: "apps",
		Description:    "Privacy-friendly web analytics (Community Edition), with PostgreSQL and ClickHouse.",
		ComposeContent: composeFile("plausible"), RouteService: "plausible", DefaultPort: 8000, ComposeServices: 3, SetupLogin: true,
		Volumes: []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "SECRET_KEY_BASE", Description: "Signs sessions; Plausible derives its TOTP key from it", IsSecret: true, Generate: "long"},
			{Key: "POSTGRES_PASSWORD", Description: "Password of its PostgreSQL", IsSecret: true},
		},
	},
	{
		Kind: KindCompose, Slug: "n8n", Name: "n8n", Category: "apps",
		Description:    "Workflow automation, with PostgreSQL.",
		ComposeContent: composeFile("n8n"), RouteService: "n8n", DefaultPort: 5678, ComposeServices: 2, SetupLogin: true,
		Volumes: []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "N8N_ENCRYPTION_KEY", Description: "Encrypts the credentials n8n stores; keep it to keep them", IsSecret: true, Generate: "long"},
			{Key: "POSTGRES_PASSWORD", Description: "Password of its PostgreSQL", IsSecret: true},
		},
	},
	{
		Kind: KindCompose, Slug: "ghost", Name: "Ghost", Category: "apps",
		Description:    "Publishing and newsletters, with MySQL. Add mail settings (mail__*) before inviting staff.",
		ComposeContent: composeFile("ghost"), RouteService: "ghost", DefaultPort: 2368, ComposeServices: 2, SetupLogin: true,
		Volumes: []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "MYSQL_ROOT_PASSWORD", Description: "MySQL's root password", IsSecret: true},
			{Key: "MYSQL_PASSWORD", Description: "Password Ghost signs in to MySQL with", IsSecret: true},
		},
	},
	{
		Kind: KindCompose, Slug: "umami", Name: "Umami", Category: "apps",
		Description:    "Simple web analytics, with PostgreSQL.",
		ComposeContent: composeFile("umami"), RouteService: "umami", DefaultPort: 3000, ComposeServices: 3,
		Volumes: []VolumeMount{},
		EnvVarSpecs: []EnvVarSpec{
			{Key: "APP_SECRET", Description: "Signs sessions", IsSecret: true, Generate: "long"},
			{Key: "TWO_FACTOR_ENCRYPTION_KEY", Description: "Encrypts two-factor secrets", IsSecret: true, Generate: "hex64"},
			{Key: "POSTGRES_PASSWORD", Description: "Password of its PostgreSQL", IsSecret: true},
			// Umami creates admin/umami at its first start, and no variable
			// sets it: the admin-password service replaces that password.
			{Key: "UMAMI_ADMIN_PASSWORD", Description: "Password of its admin account; Toft sets it at the first start", IsSecret: true},
		},
		AdminPassword:  "UMAMI_ADMIN_PASSWORD",
		AdminUserFixed: "admin",
		// SQL to psql in its database, the password a psql variable on stdin
		// bound as a parameter. 1 is the one account set.
		AdminReset: &AdminReset{
			Container: "db",
			Argv: func(string) []string {
				return []string{"psql", "-U", "umami", "-d", "umami", "-v", "ON_ERROR_STOP=1", "-qtA"}
			},
			Env:    []string{"PGOPTIONS=-c log_min_error_statement=panic -c client_min_messages=warning"},
			Stdin:  umamiReset,
			Prints: "1",
		},
	},
})

// umamiReset sets Umami's admin password (hashed as Umami's admin-password
// job does) and prints how many accounts it set. The password is a bind
// parameter: never in the statement's text, so neither psql's error excerpt
// nor pg_stat_activity shows it.
func umamiReset(password string) string {
	quoted := strings.ReplaceAll(strings.ReplaceAll(password, `\`, `\\`), "'", "''")
	return `\set pw '` + quoted + "'\n" +
		"CREATE EXTENSION IF NOT EXISTS pgcrypto;\n" +
		`WITH u AS (UPDATE "user" SET password = crypt($1, gen_salt('bf', 10)) WHERE username = 'admin' RETURNING 1) SELECT count(*) FROM u \bind :pw \g` + "\n"
}
