package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gastownhall/gascity/internal/citylayout"
)

type managedDoltRuntimeLayout struct {
	PackStateDir string
	DataDir      string
	LogFile      string
	StateFile    string
	PIDFile      string
	LockFile     string
	ConfigFile   string
}

func resolveManagedDoltRuntimeLayout(cityPath string) (managedDoltRuntimeLayout, error) {
	cityPath = filepath.Clean(strings.TrimSpace(cityPath))
	if cityPath == "" || cityPath == "." {
		return managedDoltRuntimeLayout{}, fmt.Errorf("missing --city")
	}
	cityPath = normalizePathForCompare(cityPath)

	useEnvPaths := managedDoltRuntimePathEnvAppliesToCity(cityPath)
	packStateDir := ""
	if useEnvPaths {
		packStateDir = strings.TrimSpace(os.Getenv("GC_PACK_STATE_DIR"))
	}
	if packStateDir == "" {
		if runtimeDir := trustedManagedDoltRuntimeDir(useEnvPaths); runtimeDir != "" {
			packStateDir = filepath.Join(runtimeDir, "packs", "dolt")
		} else {
			packStateDir = citylayout.PackStateDir(cityPath, "dolt")
		}
	}
	dataDir := defaultEnvPath(useEnvPaths, "GC_DOLT_DATA_DIR", filepath.Join(cityPath, ".beads", "dolt"))
	logFile := defaultEnvPath(useEnvPaths, "GC_DOLT_LOG_FILE", filepath.Join(packStateDir, "dolt.log"))
	stateFile := defaultEnvPath(useEnvPaths, "GC_DOLT_STATE_FILE", filepath.Join(packStateDir, "dolt-provider-state.json"))
	pidFile := defaultEnvPath(useEnvPaths, "GC_DOLT_PID_FILE", filepath.Join(packStateDir, "dolt.pid"))
	lockFile := defaultEnvPath(useEnvPaths, "GC_DOLT_LOCK_FILE", filepath.Join(packStateDir, "dolt.lock"))
	configFile := defaultEnvPath(useEnvPaths, "GC_DOLT_CONFIG_FILE", filepath.Join(packStateDir, "dolt-config.yaml"))

	return managedDoltRuntimeLayout{
		PackStateDir: packStateDir,
		DataDir:      dataDir,
		LogFile:      logFile,
		StateFile:    stateFile,
		PIDFile:      pidFile,
		LockFile:     lockFile,
		ConfigFile:   configFile,
	}, nil
}

func managedDoltRuntimePathEnvAppliesToCity(cityPath string) bool {
	sawCityAnchor := false
	for _, key := range []string{"GC_CITY_PATH", "GC_CITY", "GC_CITY_ROOT"} {
		value := strings.TrimSpace(os.Getenv(key))
		if value == "" {
			continue
		}
		sawCityAnchor = true
		if !samePath(value, cityPath) {
			return false
		}
	}
	return sawCityAnchor
}

func trustedManagedDoltRuntimeDir(useEnvPaths bool) string {
	if !useEnvPaths {
		return ""
	}
	return strings.TrimSpace(os.Getenv("GC_CITY_RUNTIME_DIR"))
}

func defaultEnvPath(useEnvPaths bool, key, fallback string) string {
	if useEnvPaths {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return normalizePathForCompare(value)
		}
	}
	return normalizePathForCompare(fallback)
}

func doltRuntimeLayoutFields(layout managedDoltRuntimeLayout) []string {
	return []string{
		"GC_PACK_STATE_DIR\t" + layout.PackStateDir,
		"GC_DOLT_DATA_DIR\t" + layout.DataDir,
		"GC_DOLT_LOG_FILE\t" + layout.LogFile,
		"GC_DOLT_STATE_FILE\t" + layout.StateFile,
		"GC_DOLT_PID_FILE\t" + layout.PIDFile,
		"GC_DOLT_LOCK_FILE\t" + layout.LockFile,
		"GC_DOLT_CONFIG_FILE\t" + layout.ConfigFile,
	}
}
