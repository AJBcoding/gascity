"""Hermetic transaction tests; no real city or provider is modified."""

from contextlib import redirect_stdout, redirect_stderr
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import tomllib
import unittest
from unittest.mock import patch

from anthony_profile import ACTIVE, PREVIOUS, Error, Switcher, atomic_write, compare_launch, main, observe_panes, parse_variant, replacement_view, run_command


CLAUDE = b'''[providers.lane_worker]
base = "provider:claude"
option_defaults = { model = "claude-sonnet-5", effort = "low" }
'''
CODEX = b'''[providers.lane_worker]
base = "provider:codex_account"
option_defaults = { model = "gpt-5.6-luna", effort = "" }
args_append = ["--profile", "gc-medium"]
'''


class FakeGC:
    """Only CLI boundary is substituted; real temporary files are used."""

    def __init__(self, home):
        self.home = home
        self.calls = []
        self.fail_validate = False
        self.reload_failure = False
        self.reload_warning = False
        self.agent_override = {}
        self.after_validate = None

    def __call__(self, args, timeout=45):
        self.calls.append(args)
        city = Path(args[2])
        command = args[3:]
        if command[:2] == ["config", "show"]:
            if self.fail_validate:
                raise Error("unmatched patch")
            variant = tomllib.loads((city / ACTIVE).read_text())["providers"]
            providers = {name: {"Base": p["base"], "OptionDefaults": p["option_defaults"],
                                "ArgsAppend": p.get("args_append")} for name, p in variant.items()}
            config = {"Providers": providers, "Workspace": {"Provider": "claude"},
                      "Agents": [{"Name": "worker", "Dir": "rig", "Provider": "lane_worker",
                                  **self.agent_override},
                                 {"Name": "pinned", "Dir": "rig", "Provider": "codex_account"}]}
            if self.after_validate:
                self.after_validate()
            return subprocess.CompletedProcess(args, 0, json.dumps({"ok": True, "config": config}), "")
        if command == ["config", "explain"]:
            return subprocess.CompletedProcess(args, 0, "Agent: rig/pack.worker\nAgent: rig/pack.pinned\n", "")
        if command[:3] == ["config", "explain", "--provider"]:
            name = command[3]
            p = tomllib.loads((city / ACTIVE).read_text())["providers"][name]
            ancestor = "codex" if "codex" in p["base"] else "claude"
            resolved = {"command": ancestor, "args": p.get("args_append", []),
                        "option_defaults": p["option_defaults"],
                        "env": {"CODEX_HOME": str(self.home)} if ancestor == "codex" else {}}
            return subprocess.CompletedProcess(args, 0, json.dumps({"ok": True, "builtin_ancestor": ancestor,
                                                                    "resolved": resolved}), "")
        if command[0] == "reload":
            if self.reload_failure:
                raise Error("reload timeout")
            warning = "soft reload: affected sessions may still drain" if self.reload_warning else ""
            return subprocess.CompletedProcess(args, 0, "reload applied\n", warning)
        raise AssertionError(args)


class CityTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.city = self.root / "city"
        self.city.mkdir()
        (self.city / ".gc").mkdir()
        (self.city / "city.toml").write_text('include = [".gc/provider-lanes.toml"]\n')
        (self.city / ACTIVE).write_bytes(CLAUDE)
        (self.city / ".gc/untouched").write_text("untouched")
        (self.city / ".gc/site.toml").write_text("# site binding input\n")
        (self.city / "pack").mkdir()
        (self.city / "pack/data").write_text("unchanged")
        self.home = self.root / "account"
        self.home.mkdir()
        (self.home / "gc-medium.config.toml").write_text('model_reasoning_effort = "medium"\n')
        self.runner = FakeGC(self.home)
        self.switcher = Switcher(self.city, run=self.runner)

    def test_replacement_is_not_overlay_and_cleans_up(self):
        with replacement_view(self.city, CODEX) as scratch:
            self.assertEqual((scratch / ACTIVE).read_bytes(), CODEX)
            self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
            self.assertEqual((scratch / "pack/data").read_text(), "unchanged")
            self.assertFalse((scratch / ".gc/untouched").exists())
            self.assertEqual((scratch / ".gc/site.toml").read_text(), "# site binding input\n")
            self.assertFalse((scratch / ".gc/site.toml").is_symlink())
            self.assertFalse((scratch / "city.toml").is_symlink())
            self.assertFalse((scratch / ".gc").is_symlink())
        self.assertFalse(scratch.exists())

    def test_replacement_cleans_up_on_exception(self):
        with self.assertRaisesRegex(ValueError, "injected"):
            with replacement_view(self.city, CODEX) as scratch:
                raise ValueError("injected")
        self.assertFalse(scratch.exists())

    def test_preview_uses_replacement_and_reports_membership(self):
        preview = self.switcher.preview(CODEX)
        self.assertEqual(preview["managed"], ["rig/pack.worker"])
        self.assertEqual(preview["unmanaged"], ["rig/pack.pinned"])
        self.assertEqual(preview["lanes"]["lane_worker"]["model"], "gpt-5.6-luna")
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertFalse((self.city / PREVIOUS).exists())
        self.assertTrue(all("-f" not in call for call in self.runner.calls))

    def test_apply_saves_previous_and_only_soft_reloads(self):
        result = self.switcher.apply(CODEX)
        self.assertEqual(result["reload"], "confirmed")
        self.assertEqual((self.city / ACTIVE).read_bytes(), CODEX)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)
        reloads = [c[3:] for c in self.runner.calls if c[3] == "reload"]
        self.assertEqual(reloads, [["reload", "--soft", "--timeout", "45s"]])

    def test_retry_does_not_destroy_undo(self):
        self.switcher.apply(CODEX)
        self.switcher.apply(CODEX)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)
        self.assertEqual(sum(c[3] == "reload" for c in self.runner.calls), 2)

    def test_undo_validates_and_retains_recovery_target(self):
        self.switcher.apply(CODEX)
        self.switcher.undo()
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)

    def test_failed_undo_keeps_its_only_recovery_copy(self):
        self.switcher.apply(CODEX)
        with patch("anthony_profile.atomic_write", side_effect=OSError("injected write failure")):
            result = self.switcher.undo()
        self.assertEqual(result["reload"], "unconfirmed")
        self.assertEqual((self.city / ACTIVE).read_bytes(), CODEX)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)
        self.switcher.undo()
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)

    def test_undo_failure_after_rename_is_explicit_and_retryable(self):
        self.switcher.apply(CODEX)
        def after_rename(path, data):
            atomic_write(path, data)
            raise OSError("injected directory sync failure")
        with patch("anthony_profile.atomic_write", side_effect=after_rename):
            result = self.switcher.undo()
        self.assertEqual(result["reload"], "unconfirmed")
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)
        self.switcher.undo()
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)

    def test_failed_backup_never_activates(self):
        with patch("anthony_profile.atomic_write", side_effect=OSError("backup failed")):
            with self.assertRaisesRegex(OSError, "backup failed"):
                self.switcher.apply(CODEX)
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertFalse(any(c[3] == "reload" for c in self.runner.calls))

    def test_symlinked_active_and_runtime_root_are_rejected(self):
        target = self.city / "pack/data"
        (self.city / ACTIVE).unlink()
        (self.city / ACTIVE).symlink_to(target)
        with self.assertRaisesRegex(Error, "symlink"):
            self.switcher.preview(CODEX)
        self.assertEqual(target.read_text(), "unchanged")

    def test_scratch_runtime_writes_cannot_reach_city(self):
        scripts = self.city / ".gc/scripts"
        scripts.mkdir()
        (scripts / "gc-beads-bd.sh").write_text("live asset")
        with replacement_view(self.city, CODEX) as scratch:
            (scratch / ".gc/scripts").mkdir(exist_ok=True)
            (scratch / ".gc/scripts/gc-beads-bd.sh").write_text("generated scratch asset")
        self.assertEqual((scripts / "gc-beads-bd.sh").read_text(), "live asset")

    def test_gc_failure_keeps_both_output_streams(self):
        completed = subprocess.CompletedProcess(["gc"], 1, "failed sessions may still drain", "other error")
        with patch("subprocess.run", return_value=completed), self.assertRaises(Error) as context:
            run_command(["gc", "reload", "--soft"])
        self.assertIn("may still drain", str(context.exception))
        self.assertIn("other error", str(context.exception))

    def test_timeout_keeps_warning_output(self):
        failure = subprocess.TimeoutExpired(["gc"], 45, output=b"may still drain", stderr=b"acceptance pending")
        with patch("subprocess.run", side_effect=failure), self.assertRaises(Error) as context:
            run_command(["gc", "reload", "--soft"])
        self.assertIn("may still drain", str(context.exception))
        self.assertIn("acceptance pending", str(context.exception))

    def test_nested_validation_warnings_are_reported(self):
        def run(args, timeout=45):
            return subprocess.CompletedProcess(args, 0, '{"ok":true,"validation":{"warnings":["singleton drains"]}}', "")
        switcher = Switcher(self.city, run=run)
        switcher.json_command(self.city, "config", "show", "--json")
        self.assertEqual(switcher.warnings, ["singleton drains"])

    def test_failed_validation_does_not_activate(self):
        self.runner.fail_validate = True
        with self.assertRaisesRegex(Error, "unmatched"):
            self.switcher.apply(CODEX)
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertFalse((self.city / PREVIOUS).exists())
        self.assertFalse(any(c[3] == "reload" for c in self.runner.calls))

    def test_failed_reload_retains_active_and_undo_and_reports_ambiguity(self):
        self.runner.reload_failure = True
        result = self.switcher.apply(CODEX)
        self.assertEqual(result["reload"], "unconfirmed")
        self.assertIn("timeout", result["diagnostic"])
        self.assertEqual((self.city / ACTIVE).read_bytes(), CODEX)
        self.assertEqual((self.city / PREVIOUS).read_bytes(), CLAUDE)

    def test_reload_warnings_are_not_success(self):
        self.runner.reload_warning = True
        result = self.switcher.apply(CODEX)
        self.assertEqual(result["reload"], "unconfirmed")
        self.assertIn("may still drain", result["diagnostic"])

    def test_concurrent_root_edit_rejected_before_activation(self):
        self.runner.after_validate = lambda: (self.city / "city.toml").write_text("# changed\n")
        with self.assertRaisesRegex(Error, "changed"):
            self.switcher.apply(CODEX)
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)

    def test_symlinked_previous_is_rejected(self):
        (self.city / PREVIOUS).symlink_to(self.city / "pack/data")
        with self.assertRaisesRegex(Error, "symlink"):
            self.switcher.apply(CODEX)
        self.assertEqual((self.city / "pack/data").read_text(), "unchanged")

    def test_missing_include_rejected(self):
        (self.city / "city.toml").write_text("")
        with self.assertRaisesRegex(Error, "include"):
            self.switcher.apply(CODEX)

    def test_agent_model_and_effort_overrides_rejected_even_when_empty(self):
        for key in ("model", "effort"):
            with self.subTest(key=key):
                self.runner.agent_override = {"OptionDefaults": {key: ""}}
                with self.assertRaisesRegex(Error, "remove.*" + key):
                    self.switcher.preview(CODEX)

    def test_custom_resume_or_launch_rejected(self):
        for key in ("ResumeCommand", "StartCommand", "Args"):
            with self.subTest(key=key):
                self.runner.agent_override = {key: ["--model", "old"] if key == "Args" else "custom"}
                with self.assertRaisesRegex(Error, key):
                    self.switcher.preview(CODEX)

    def test_codex_effort_must_be_empty_and_profile_must_exist(self):
        with self.assertRaisesRegex(Error, "effort"):
            self.switcher.preview(CODEX.replace(b'effort = ""', b'effort = "high"'))
        (self.home / "gc-medium.config.toml").unlink()
        with self.assertRaisesRegex(Error, "profile"):
            self.switcher.preview(CODEX)

    def test_profile_cannot_override_lane_model(self):
        (self.home / "gc-medium.config.toml").write_text('model = "wrong"\nmodel_reasoning_effort = "medium"\n')
        with self.assertRaisesRegex(Error, "effort-only"):
            self.switcher.preview(CODEX)

    def test_profile_remains_usable_after_codex_writes_bookkeeping(self):
        (self.home / "gc-medium.config.toml").write_text('''model_reasoning_effort = "medium"
[tui.model_availability_nux]
gpt-6-astra = 3
[hooks.state."/account/hooks.json:session_start:0:0"]
trusted_hash = "sha256:535e026163ed106cb01d77dd2f7e4cb220202686a6777b76d3138daf7a8557e3"
''')
        result = self.switcher.preview(CODEX)
        self.assertEqual(result["lanes"]["lane_worker"]["effort"], "medium")

    def test_profile_bookkeeping_does_not_allow_behavior_overrides(self):
        additions = (
            'model = "wrong"',
            'openai_base_url = "https://example.invalid"',
            '[hooks.SessionStart]\ncommand = "do-something"',
            '[hooks.state.entry]\ncommand = "do-something"',
            '[hooks.state.entry]\ntrusted_hash = "not-a-hash"',
            '[tui]\nmodel = "wrong"',
            '[tui.model_availability_nux]\nmodel = "wrong"',
            '[tui.model_availability_nux]\nmodel = true',
            'hooks = "wrong-type"',
            '[hooks]\nstate = ["wrong-type"]',
            'tui = ["wrong-type"]',
            '[tui]\nmodel_availability_nux = "wrong-type"',
        )
        for addition in additions:
            with self.subTest(addition=addition):
                (self.home / "gc-medium.config.toml").write_text(
                    'model_reasoning_effort = "medium"\n' + addition + '\n')
                with self.assertRaisesRegex(Error, "effort-only"):
                    self.switcher.preview(CODEX)

    def test_variant_scope_and_complete_defaults(self):
        for variant in (b'[[patches.agent]]\nname="worker"',
                        CLAUDE.replace(b"lane_worker", b"codex_account"),
                        CLAUDE.replace(b', effort = "low"', b""),
                        CLAUDE + b'command = "shell"\n'):
            with self.subTest(variant=variant), self.assertRaises(Error):
                parse_variant(variant)

    def test_shipped_examples_have_explicit_complete_lanes(self):
        example_dir = Path(__file__).parent / "examples"
        claude = parse_variant((example_dir / "claude.toml").read_bytes())
        codex = parse_variant((example_dir / "codex.toml").read_bytes())
        self.assertEqual(set(claude), set(codex))
        self.assertEqual(len(claude), 4)
        self.assertTrue(all(p["option_defaults"]["effort"] == "" for p in codex.values()))


class ObservationTest(unittest.TestCase):
    def test_known_claude_effort_mismatch_is_pending_cutover(self):
        desired = {"provider": "claude", "model": "claude-opus-5", "effort": "low"}
        observed = {"provider": "claude", "model": "claude-opus-5", "effort": "high"}
        self.assertEqual(compare_launch(desired, observed), "pending-cutover")

    def test_missing_claude_effort_is_not_verified(self):
        desired = {"provider": "claude", "model": "claude-opus-5", "effort": "low"}
        observed = {"provider": "claude", "model": "claude-opus-5"}
        self.assertEqual(compare_launch(desired, observed), "unverified")

    def test_orphaned_template_is_not_preserved(self):
        switcher = Switcher(Path("/city"))
        config = {"Providers": {}, "Agents": []}
        sessions = {"sessions": [{"id": "az-123", "template": "removed", "state": "active"}]}
        with patch.object(switcher, "load_config", return_value=config), patch.object(switcher, "json_command", return_value=sessions), patch("anthony_profile.observe_panes", return_value=([], [])):
            result = switcher.status()
        self.assertEqual(result["seats"][0]["verdict"], "unverified")
        self.assertTrue(any("absent" in problem for problem in result["problems"]))

    def test_status_detects_agent_override_drift(self):
        switcher = Switcher(Path("/city"))
        config = {"Providers": {"lane_worker": {"Base": "provider:claude", "OptionDefaults": {"model": "claude-sonnet-5", "effort": "low"}}},
                  "Agents": [{"identity": "worker", "Provider": "lane_worker", "OptionDefaults": {"model": "claude-opus-5"}}]}
        sessions = {"sessions": [{"id": "az-123", "template": "worker", "state": "active"}]}
        desired = {"provider": "claude", "model": "claude-sonnet-5", "effort": "low"}
        observed = [{"session_id": "az-123", "template": "worker", **desired}]
        with patch.object(switcher, "load_config", return_value=config), patch.object(switcher, "json_command", return_value=sessions), patch.object(switcher, "lane_settings", return_value=desired), patch("anthony_profile.observe_panes", return_value=(observed, [])):
            result = switcher.status()
        self.assertEqual(result["seats"][0]["verdict"], "unverified")
        self.assertTrue(any("agent-level model" in problem for problem in result["problems"]))
        self.assertIsNone(result["seats"][0]["desired"])

    def test_human_status_prints_unlinked_process_and_exits_nonzero(self):
        result = {"seats": [], "unlinked_processes": [{"pid": 42, "session_id": "az-orphan"}],
                  "problems": ["unlinked"], "warnings": [], "note": "snapshot"}
        stdout, stderr = io.StringIO(), io.StringIO()
        with patch.object(Switcher, "status", return_value=result), redirect_stdout(stdout), redirect_stderr(stderr):
            code = main(["--city", "/city", "status"])
        self.assertEqual(code, 2)
        self.assertIn("az-orphan", stdout.getvalue())
    def test_bead_provider_is_not_launch_evidence(self):
        desired = {"provider": "codex", "model": "gpt-5.6-luna", "home": "/account", "profile": "gc-medium"}
        observed = {"provider": "claude", "model": "claude-opus-5", "home": None, "profile": None}
        self.assertEqual(compare_launch(desired, observed), "pending-cutover")
        self.assertEqual(compare_launch(desired, None), "unverified")
        self.assertEqual(compare_launch(desired, desired), "launch-matches; effective settings unverified")

    def test_missing_model_flag_is_not_match(self):
        self.assertEqual(compare_launch({"provider": "codex", "model": "m"}, {"provider": "codex", "model": None}), "unverified")

    def test_herdr_and_process_environment_correlate_without_leaking_secrets(self):
        def run(args, timeout=45):
            if "list" in args:
                payload = {"result": {"panes": [{"pane_id": "pane-1"}]}}
            elif "process-info" in args:
                payload = {"result": {"process_info": {"foreground_processes": [
                    {"pid": 42, "argv": ["codex", "--model", "gpt-5.6-luna", "--profile", "gc-medium"]}]}}}
            else:
                self.assertEqual(args, ["ps", "eww", "-p", "42", "-o", "command="])
                return subprocess.CompletedProcess(args, 0,
                    "codex SECRET_TOKEN=DO_NOT_PRINT GC_CITY=/city GC_SESSION_ID=az-123 "
                    "GC_TEMPLATE=rig/pack.worker CODEX_HOME=/account", "")
            return subprocess.CompletedProcess(args, 0, json.dumps(payload), "")
        observations, problems = observe_panes(Path("/city"), "anthony", run)
        self.assertEqual(problems, [])
        self.assertEqual(observations[0]["session_id"], "az-123")
        self.assertEqual(observations[0]["home"], "/account")
        self.assertEqual(observations[0]["model"], "gpt-5.6-luna")
        self.assertNotIn("DO_NOT_PRINT", json.dumps(observations))

    def test_unknown_runtime_is_not_empty_success(self):
        def unavailable(args, timeout=45):
            raise Error("herdr unavailable")
        observations, problems = observe_panes(Path("/city"), "anthony", unavailable)
        self.assertEqual(observations, [])
        self.assertIn("herdr unavailable", problems[0])


@unittest.skipUnless(os.environ.get("ANTHONY_PROFILE_TEST_GC"), "set ANTHONY_PROFILE_TEST_GC to test the installed binary")
class InstalledGCTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="anthony-profile-integration-")
        self.addCleanup(self.temp.cleanup)
        self.city = Path(self.temp.name)
        (self.city / ".gc").mkdir()
        (self.city / "account").mkdir()
        (self.city / "account/gc-medium.config.toml").write_text('model_reasoning_effort = "medium"\n')
        (self.city / "pack").mkdir()
        (self.city / "pack/pack.toml").write_text('''[pack]
name = "probe"
schema = 2
version = "0.1.0"
[[agent]]
name = "worker"
scope = "city"
provider = "lane_worker"
''')
        (self.city / "city.toml").write_text(f'''include = [".gc/provider-lanes.toml"]
[workspace]
name = "profile-probe"
provider = "claude"
[imports.probe]
source = "./pack"
[providers.claude]
base = "builtin:claude"
[providers.codex_account]
base = "builtin:codex"
env = {{ CODEX_HOME = {json.dumps(str(self.city / "account"))} }}
''')
        (self.city / ACTIVE).write_bytes(CLAUDE)
        self.gc = os.environ["ANTHONY_PROFILE_TEST_GC"]
        self.switcher = Switcher(self.city, gc=self.gc)

    def test_replacement_validates_both_providers_and_relative_pack(self):
        before = self.switcher.preview(CLAUDE)
        after = self.switcher.preview(CODEX)
        self.assertEqual(before["managed"], ["lane_worker", "probe.worker"])
        self.assertEqual(after["managed"], before["managed"])
        self.assertEqual(after["lanes"]["lane_worker"]["model"], "gpt-5.6-luna")
        self.assertEqual(after["lanes"]["lane_worker"]["effort"], "medium")
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)

    def test_unsafe_agent_pin_rejected_before_activation(self):
        pack = self.city / "pack/pack.toml"
        pack.write_text(pack.read_text() + 'option_defaults = {model="claude-sonnet-5", effort="low"}\n')
        with self.assertRaises(Error):
            self.switcher.apply(CODEX)
        self.assertEqual((self.city / ACTIVE).read_bytes(), CLAUDE)
        self.assertFalse((self.city / PREVIOUS).exists())

    def test_raw_gc_accepts_stale_model_so_helper_guard_is_required(self):
        pack = self.city / "pack/pack.toml"
        pack.write_text(pack.read_text() + 'option_defaults = {model="claude-sonnet-5", effort="low"}\n')
        with replacement_view(self.city, CODEX) as scratch:
            result = run_command([self.gc, "--city", str(scratch), "config", "show", "--validate", "--json"])
            self.assertTrue(json.loads(result.stdout)["ok"])
        with self.assertRaisesRegex(Error, "remove agent-level model"):
            self.switcher.preview(CODEX)

    def test_duplicate_lane_cannot_mask_candidate(self):
        root = self.city / "city.toml"
        root.write_text(root.read_text() + CLAUDE.decode())
        with self.assertRaisesRegex(Error, "loaded candidate differs"):
            self.switcher.preview(CODEX)

    def test_concrete_alias_cannot_inherit_lane_using_bare_base(self):
        root = self.city / "city.toml"
        root.write_text(root.read_text() + '\n[providers.pinned_alias]\nbase = "lane_worker"\n')
        with self.assertRaisesRegex(Error, "concrete providers must not inherit lanes"):
            self.switcher.preview(CODEX)

    def test_missing_runtime_pack_cannot_silently_shrink_membership(self):
        pack = self.city / ".gc/legacy"
        pack.mkdir()
        (pack / "pack.toml").write_text('[[agent]]\nname="hidden"\nprovider="lane_worker"\noption_defaults={model="claude-opus-5"}\n')
        root = self.city / "city.toml"
        root.write_text(root.read_text().replace('[workspace]\n', '[workspace]\nincludes=[".gc/legacy"]\n'))
        with self.assertRaisesRegex(Error, "skipping|composition"):
            self.switcher.preview(CODEX)

    def test_skipped_pack_diagnostic_in_nested_fragment_rejects_preview(self):
        root = self.city / "city.toml"
        root.write_text(root.read_text().replace('include = [".gc/provider-lanes.toml"]',
                                                'include = ["extra.toml", ".gc/provider-lanes.toml"]'))
        (self.city / "extra.toml").write_text('[workspace]\nincludes=[".gc/legacy"]\n')
        with self.assertRaisesRegex(Error, "composition"):
            self.switcher.preview(CODEX)

    def test_gc_stacked_overlay_does_not_validate_replacement(self):
        overlay = self.city / "codex.toml"
        overlay.write_bytes(CODEX)
        command = [self.gc, "--city", str(self.city), "config", "explain", "-f", str(overlay),
                   "--provider", "lane_worker", "--json"]
        actual = json.loads(run_command(command).stdout)["resolved"]["option_defaults"]
        self.assertEqual(actual["model"], "claude-sonnet-5", "if deployed gc fixes provider merge, revisit replacement requirement")

    @unittest.skipUnless(os.environ.get("ANTHONY_PROFILE_TEST_CITY"), "set ANTHONY_PROFILE_TEST_CITY for a read-only city snapshot check")
    def test_full_city_snapshot_replacement_preserves_real_inputs(self):
        source = Path(os.environ["ANTHONY_PROFILE_TEST_CITY"]).resolve()
        original = (source / "city.toml").read_bytes()
        self.assertNotIn("include", tomllib.loads(original.decode()), "snapshot test expects a pre-install city")
        snapshot = self.city / "snapshot"
        snapshot.mkdir()
        (snapshot / ".gc").mkdir()
        for entry in source.iterdir():
            if entry.name not in (".gc", "city.toml"):
                (snapshot / entry.name).symlink_to(entry, target_is_directory=entry.is_dir())
        site = source / ".gc/site.toml"
        if site.exists():
            (snapshot / ".gc/site.toml").write_bytes(site.read_bytes())
        (snapshot / "city.toml").write_bytes(b'include = [".gc/provider-lanes.toml"]\n' + original)
        (snapshot / ACTIVE).write_bytes(CLAUDE)
        candidate = CLAUDE.replace(b"claude-sonnet-5", b"claude-opus-5")
        result = Switcher(snapshot, gc=self.gc).preview(candidate)
        self.assertEqual(result["lanes"]["lane_worker"]["model"], "claude-opus-5")
        self.assertEqual(len(result["managed"]), len(tomllib.loads(original.decode()).get("rigs", [])) + 1)
        self.assertTrue(all(name.rsplit("/", 1)[-1] == "lane_worker" for name in result["managed"]))
        self.assertIn("python419/gastown.polecat", result["unmanaged"])
        self.assertEqual((source / "city.toml").read_bytes(), original)
        self.assertEqual((snapshot / ACTIVE).read_bytes(), CLAUDE)


if __name__ == "__main__":
    unittest.main()
