package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gastownhall/gascity/internal/landing"
	"github.com/spf13/cobra"
)

var (
	landingOpenEventsProvider = openCityEventsProvider
	landingNewObserver        = func() landing.RemoteObserver {
		observer := newGitLandingObserver()
		return observer
	}
)

type landingReceiptJSON struct {
	SchemaVersion         string                         `json:"schema_version,omitempty"`
	WorkflowID            string                         `json:"workflow_id"`
	IntegrationAttemptID  string                         `json:"integration_attempt_id"`
	RepositoryPath        string                         `json:"repository_path"`
	Repository            string                         `json:"repository"`
	Remote                string                         `json:"remote"`
	TargetRef             string                         `json:"target_ref"`
	ExpectedTargetSHA     string                         `json:"expected_target_sha"`
	ApprovedCandidateSHA  string                         `json:"approved_candidate_sha"`
	ExpectedLandedSHA     string                         `json:"expected_landed_sha"`
	PublicationMode       string                         `json:"publication_mode"`
	IntegrationResultPath string                         `json:"integration_result_path"`
	IntegrationResultHash string                         `json:"integration_result_hash"`
	WorkBeadIDs           []string                       `json:"work_bead_ids,omitempty"`
	WorkRecords           []landingReceiptWorkRecordJSON `json:"work_records,omitempty"`
}

type landingReceiptWorkRecordJSON struct {
	StoreRef   string `json:"store_ref"`
	BeadID     string `json:"bead_id"`
	WorkCommit string `json:"work_commit"`
}

type landingRecordJSONResult struct {
	SchemaVersion     string `json:"schema_version"`
	OK                bool   `json:"ok"`
	EventID           string `json:"event_id"`
	ObservedLandedSHA string `json:"observed_landed_sha"`
	AlreadyRecorded   bool   `json:"already_recorded"`
}

func newLandingCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "landing",
		Short: "Verify and record authoritative landing observations",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newLandingRecordCmd(stdout, stderr))
	cmd.AddCommand(newLandingStampCmd(stdout, stderr))
	return cmd
}

func newLandingRecordCmd(stdout, stderr io.Writer) *cobra.Command {
	var receiptPath string
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "record --receipt <absolute-json-path>",
		Short: "Observe an exact remote ref and record typed landing evidence",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if receiptPath == "" {
				fmt.Fprintln(stderr, "gc landing record: --receipt is required") //nolint:errcheck // command already fails
				return errExit
			}
			if !filepath.IsAbs(receiptPath) {
				fmt.Fprintln(stderr, "gc landing record: --receipt must be an absolute path") //nolint:errcheck // command already fails
				return errExit
			}
			receipt, err := readLandingReceipt(filepath.Clean(receiptPath))
			if err != nil {
				fmt.Fprintf(stderr, "gc landing record: %v\n", err) //nolint:errcheck // command already fails
				return errExit
			}

			provider, code := landingOpenEventsProvider(stderr, "gc landing record")
			if provider == nil {
				_ = code
				return errExit
			}
			defer provider.Close() //nolint:errcheck // operation has already confirmed its read-back

			result, err := (&landing.Service{
				Observer: landingNewObserver(),
				Journal:  provider,
			}).Record(command.Context(), landing.RecordRequest{
				ReceiptVersion:        receipt.SchemaVersion,
				WorkflowID:            receipt.WorkflowID,
				IntegrationAttemptID:  receipt.IntegrationAttemptID,
				RepositoryPath:        receipt.RepositoryPath,
				Repository:            receipt.Repository,
				Remote:                receipt.Remote,
				TargetRef:             receipt.TargetRef,
				ExpectedTargetSHA:     receipt.ExpectedTargetSHA,
				ApprovedCandidateSHA:  receipt.ApprovedCandidateSHA,
				ExpectedLandedSHA:     receipt.ExpectedLandedSHA,
				PublicationMode:       receipt.PublicationMode,
				IntegrationResultPath: receipt.IntegrationResultPath,
				IntegrationResultHash: receipt.IntegrationResultHash,
				WorkBeadIDs:           receipt.WorkBeadIDs,
				WorkRecords:           landingReceiptWorkRecords(receipt.WorkRecords),
				Actor:                 eventActor(),
			})
			if err != nil {
				fmt.Fprintf(stderr, "gc landing record: %v\n", err) //nolint:errcheck // command already fails
				return errExit
			}

			if jsonOutput {
				return writeCLIJSONLineOrErr(stdout, stderr, "gc landing record", landingRecordJSONResult{
					SchemaVersion:     "1",
					OK:                true,
					EventID:           result.EventID,
					ObservedLandedSHA: result.ObservedLandedSHA,
					AlreadyRecorded:   result.AlreadyRecorded,
				})
			}
			status := "recorded"
			if result.AlreadyRecorded {
				status = "already recorded"
			}
			fmt.Fprintf(stdout, "Landing %s: %s at %s\n", status, result.EventID, result.ObservedLandedSHA) //nolint:errcheck // stdout best-effort
			return nil
		},
	}
	cmd.Flags().StringVar(&receiptPath, "receipt", "", "absolute path to a landing receipt JSON file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit JSON summary")
	return cmd
}

func landingReceiptWorkRecords(records []landingReceiptWorkRecordJSON) []landing.WorkRecordRef {
	if len(records) == 0 {
		return nil
	}
	result := make([]landing.WorkRecordRef, len(records))
	for index, record := range records {
		result[index] = landing.WorkRecordRef{
			StoreRef:   record.StoreRef,
			BeadID:     record.BeadID,
			WorkCommit: record.WorkCommit,
		}
	}
	return result
}

func readLandingReceipt(path string) (landingReceiptJSON, error) {
	file, err := os.Open(path)
	if err != nil {
		return landingReceiptJSON{}, fmt.Errorf("opening receipt: %w", err)
	}
	defer file.Close() //nolint:errcheck // read-only file

	raw, err := io.ReadAll(io.LimitReader(file, landing.MaxReceiptBytes+1))
	if err != nil {
		return landingReceiptJSON{}, fmt.Errorf("reading receipt: %w", err)
	}
	if len(raw) > landing.MaxReceiptBytes {
		return landingReceiptJSON{}, fmt.Errorf("receipt exceeds %d bytes", landing.MaxReceiptBytes)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var receipt landingReceiptJSON
	if err := decoder.Decode(&receipt); err != nil {
		return landingReceiptJSON{}, fmt.Errorf("decoding receipt: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return landingReceiptJSON{}, fmt.Errorf("decoding receipt: trailing JSON value")
		}
		return landingReceiptJSON{}, fmt.Errorf("decoding receipt trailing data: %w", err)
	}
	return receipt, nil
}
