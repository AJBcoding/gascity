package main

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
	"github.com/gastownhall/gascity/internal/config"
	"github.com/gastownhall/gascity/internal/coordclass"
	"github.com/gastownhall/gascity/internal/storeref"
	"github.com/gastownhall/gascity/internal/workstamp"
	"github.com/spf13/cobra"
)

var (
	landingEventIDPattern   = regexp.MustCompile(`^gcl-[0-9a-f]{64}$`)
	landingStoreNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,254}$`)
	landingNewStampResolver = newCityLandingStampResolver
)

type landingStampJSONResult struct {
	SchemaVersion  string               `json:"schema_version"`
	OK             bool                 `json:"ok"`
	EventID        string               `json:"event_id"`
	Stamped        int                  `json:"stamped"`
	AlreadyStamped int                  `json:"already_stamped"`
	Conflicts      []workstamp.Conflict `json:"conflicts"`
	Error          string               `json:"error,omitempty"`
}

func newLandingStampCmd(stdout, stderr io.Writer) *cobra.Command {
	var eventID string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "stamp --event <gcl-id>",
		Short: "Stamp exact source work records from durable landing evidence",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !landingEventIDPattern.MatchString(eventID) {
				return landingStampCommandFailure(stdout, stderr, jsonOutput, eventID, "--event must be an exact gcl- ID")
			}
			provider, _ := landingOpenEventsProvider(stderr, "gc landing stamp")
			if provider == nil {
				return landingStampCommandFailure(stdout, stderr, jsonOutput, eventID, "opening city event journal failed")
			}
			defer provider.Close() //nolint:errcheck // command verifies all durable evidence before returning

			resolver, err := landingNewStampResolver(stderr)
			if err != nil {
				return landingStampCommandFailure(stdout, stderr, jsonOutput, eventID, err.Error())
			}
			if closer, ok := resolver.(interface{ Close() error }); ok {
				defer closer.Close() //nolint:errcheck // stores have completed readback before return
			}
			result, err := (&workstamp.Service{Journal: provider, Stores: resolver}).Stamp(command.Context(), eventID)
			if err != nil {
				return landingStampCommandFailure(stdout, stderr, jsonOutput, eventID, err.Error())
			}
			output := landingStampJSONResult{
				SchemaVersion:  "1",
				OK:             len(result.Conflicts) == 0,
				EventID:        eventID,
				Stamped:        result.Stamped,
				AlreadyStamped: result.AlreadyStamped,
				Conflicts:      result.Conflicts,
			}
			if jsonOutput {
				if err := writeCLIJSONLineOrErr(stdout, stderr, "gc landing stamp", output); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(stdout, "Landing stamp %s: stamped=%d already_stamped=%d conflicts=%d\n", eventID, result.Stamped, result.AlreadyStamped, len(result.Conflicts)) //nolint:errcheck // stdout best-effort
			}
			if len(result.Conflicts) != 0 {
				return errExit
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&eventID, "event", "", "exact delivery.landed event ID")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit one JSON result")
	return cmd
}

func landingStampCommandFailure(stdout, stderr io.Writer, jsonOutput bool, eventID, message string) error {
	if jsonOutput {
		_ = writeCLIJSONLineOrErr(stdout, stderr, "gc landing stamp", landingStampJSONResult{
			SchemaVersion: "1",
			OK:            false,
			EventID:       eventID,
			Conflicts:     []workstamp.Conflict{},
			Error:         message,
		})
	} else {
		fmt.Fprintf(stderr, "gc landing stamp: %s\n", message) //nolint:errcheck // command already fails
	}
	return errExit
}

type cityLandingStampResolver struct {
	cityPath string
	cityName string
	cfg      *config.City
	stores   map[string]beads.Store
}

func newCityLandingStampResolver(stderr io.Writer) (workstamp.StoreResolver, error) {
	cityPath, err := resolveCity()
	if err != nil {
		return nil, fmt.Errorf("resolving city: %w", err)
	}
	cfg, err := loadCityConfig(cityPath, stderr)
	if err != nil {
		return nil, fmt.Errorf("loading city config: %w", err)
	}
	return &cityLandingStampResolver{
		cityPath: cityPath,
		cityName: loadedCityName(cfg, cityPath),
		cfg:      cfg,
		stores:   make(map[string]beads.Store),
	}, nil
}

func (r *cityLandingStampResolver) Resolve(ctx context.Context, storeRef string) (beads.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	kind, name, ok := strings.Cut(storeRef, ":")
	if !ok || !landingStoreNamePattern.MatchString(name) || (kind != "city" && kind != "rig" && kind != "class") {
		return nil, fmt.Errorf("store ref %q is not a canonical local city, rig, or class ref", storeRef)
	}
	if store, exists := r.stores[storeRef]; exists {
		return store, nil
	}
	storePath := ""
	switch kind {
	case "city":
		if name != r.cityName {
			return nil, fmt.Errorf("city store ref %q does not name current city %q", storeRef, r.cityName)
		}
		storePath = r.cityPath
	case "rig":
		rig := findRigByName(name, r.cfg.Rigs)
		if rig == nil || strings.TrimSpace(rig.Path) == "" {
			return nil, fmt.Errorf("rig store ref %q is not configured with a local path", storeRef)
		}
		storePath = resolveStoreScopeRoot(r.cityPath, rig.Path)
	case "class":
		routes := cliStorageRoutes(r.cityPath)
		store, relocated := graphClassBinding(routes)
		if !relocated {
			return nil, fmt.Errorf("class store ref %q is not served by a local class binding", storeRef)
		}
		classes := make([]coordclass.Class, 0, len(routes.stores))
		for class := range routes.stores {
			classes = append(classes, class)
		}
		if want := string(storeref.ClassRef(classes)); storeRef != want {
			return nil, fmt.Errorf("class store ref %q does not name current class binding %q", storeRef, want)
		}
		r.stores[storeRef] = store
		return store, nil
	}
	store, err := openStoreAtForCityWithConfig(storePath, r.cityPath, r.cfg)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", storeRef, err)
	}
	r.stores[storeRef] = store
	return store, nil
}

func (r *cityLandingStampResolver) Close() error {
	return nil
}
