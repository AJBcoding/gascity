package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/beads/contract"
	"github.com/gastownhall/gascity/internal/fsys"
	"github.com/spf13/cobra"
)

type cityMySQLOptions struct {
	DSN    string
	DryRun bool
}

type cityMySQLScopePlan struct {
	Name     string
	Path     string
	Database string
}

func newBeadsCityUseMySQLCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts cityMySQLOptions
	cmd := &cobra.Command{
		Use:   "use-mysql",
		Short: "Point the city and its rigs at an external MySQL server",
		Long: `Rewrite every bd-backed scope's metadata.json to the mysql backend.

Each scope keeps its own database: an existing mysql_database (including the
fork-era split-field shape, whose legacy mysql_host/port/user keys are
scrubbed) or, failing that, the scope's dolt_database. The DSN is stored
verbatim and passed through to bd; include no password (bd resolves
BEADS_MYSQL_PASSWORD at runtime and never persists it).`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cityPath, err := resolveCity()
			if err != nil {
				fmt.Fprintf(stderr, "gc beads city use-mysql: %v\n", err) //nolint:errcheck
				return errExit
			}
			if doBeadsCityUseMySQL(fsys.OSFS{}, cityPath, opts, stdout, stderr) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.DSN, "dsn", "", `MySQL DSN without database, e.g. "root@tcp(127.0.0.1:3306)/"`)
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "show the per-scope plan without writing files")
	return cmd
}

func doBeadsCityUseMySQL(fs fsys.FS, cityPath string, opts cityMySQLOptions, stdout, stderr io.Writer) int { //nolint:unparam // fs is the filesystem seam this command reads and writes scope metadata through; every call site passes OSFS{} today
	const name = "gc beads city use-mysql"
	if strings.TrimSpace(opts.DSN) == "" {
		fmt.Fprintf(stderr, "%s: --dsn is required (e.g. \"root@tcp(127.0.0.1:3306)/\")\n", name) //nolint:errcheck
		return 1
	}
	if !cityUsesBdStoreContract(cityPath) {
		fmt.Fprintf(stderr, "%s: only supported for bd-backed beads providers\n", name) //nolint:errcheck
		return 1
	}

	scopes := []cityMySQLScopePlan{{Name: "city", Path: cityPath}}
	if cfg, err := loadCityConfig(cityPath, io.Discard); err == nil {
		resolveRigPaths(cityPath, cfg.Rigs)
		for _, rig := range cfg.Rigs {
			rigPath := strings.TrimSpace(rig.Path)
			if rigPath == "" {
				continue
			}
			scopes = append(scopes, cityMySQLScopePlan{Name: "rig " + rig.Name, Path: rigPath})
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "%s: loading city config: %v\n", name, err) //nolint:errcheck
		return 1
	}

	for i := range scopes {
		database, err := mysqlDatabaseForScope(fs, scopes[i].Path)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %s (%s): %v\n", name, scopes[i].Name, scopes[i].Path, err) //nolint:errcheck
			return 1
		}
		scopes[i].Database = database
	}

	if opts.DryRun {
		fmt.Fprintln(stdout, "Dry run — no files written. Planned scopes:") //nolint:errcheck
		for _, scope := range scopes {
			fmt.Fprintf(stdout, "  %-12s %s -> backend=mysql database=%s dsn=%s\n", scope.Name, scope.Path, scope.Database, opts.DSN) //nolint:errcheck
		}
		return 0
	}

	for _, scope := range scopes {
		path := scopeMetadataJSONPath(scope.Path)
		if _, err := contract.EnsureCanonicalMetadata(fs, path, contract.MetadataState{
			Database:      "beads.db",
			Backend:       "mysql",
			MySQLDSN:      strings.TrimSpace(opts.DSN),
			MySQLDatabase: scope.Database,
		}); err != nil {
			fmt.Fprintf(stderr, "%s: %s (%s): %v\n", name, scope.Name, scope.Path, err) //nolint:errcheck
			return 1
		}
		fmt.Fprintf(stdout, "✓ %-12s %s -> backend=mysql database=%s\n", scope.Name, scope.Path, scope.Database) //nolint:errcheck
	}
	fmt.Fprintln(stdout, "Done. bd self-configures from each scope's metadata.json; set BEADS_MYSQL_PASSWORD in the environment if the server requires one.") //nolint:errcheck
	return 0
}

// mysqlDatabaseForScope picks the per-scope database for a mysql conversion:
// an existing mysql_database (upstream or fork-era shape), else the scope's
// dolt_database. Read from the raw JSON so fork-era metadata — which the
// strict contract loader rejects for its missing mysql_dsn — still converts.
func mysqlDatabaseForScope(fs fsys.FS, scopeRoot string) (string, error) {
	data, err := fs.ReadFile(filepath.Join(scopeRoot, ".beads", "metadata.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("scope has no .beads/metadata.json; run bd init or pass over this scope after initializing it")
		}
		return "", err
	}
	var raw struct {
		MySQLDatabase string `json:"mysql_database"`
		DoltDatabase  string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", fmt.Errorf("parse metadata.json: %w", err)
	}
	if db := strings.TrimSpace(raw.MySQLDatabase); db != "" {
		return db, nil
	}
	if db := strings.TrimSpace(raw.DoltDatabase); db != "" {
		return db, nil
	}
	return "", fmt.Errorf("no mysql_database or dolt_database in metadata.json to derive the scope database from")
}
