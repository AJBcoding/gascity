#!/usr/bin/env python3
"""Deliberate city-local lane selection; never moves running sessions."""

import argparse
from contextlib import contextmanager
import difflib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import tomllib

ACTIVE = Path(".gc/provider-lanes.toml")
PREVIOUS = Path(".gc/provider-lanes.previous.toml")


class Error(Exception):
    """A rejected operation with an operator-facing explanation."""


def run_command(args, timeout=45):
    """Run a bounded CLI request without a shell or an ambient remote context."""
    env = dict(os.environ)
    for key in ("GC_CITY", "GC_CITY_URL", "GC_CITY_NAME", "GC_CONTEXT"):
        env.pop(key, None)
    try:
        result = subprocess.run(args, capture_output=True, text=True, timeout=timeout, env=env)
    except subprocess.TimeoutExpired as exc:
        outputs = [v.decode(errors="replace") if isinstance(v, bytes) else (v or "")
                   for v in (exc.stdout, exc.stderr)]
        raise Error(f"{args[0]} timed out; completion unconfirmed\n" + "\n".join(outputs)) from exc
    except OSError as exc:
        raise Error(f"{args[0]} request failed: {exc}") from exc
    if result.returncode:
        raise Error(f"{args[0]} exited {result.returncode}:\n{result.stdout.strip()}\n{result.stderr.strip()}")
    return result


def read_toml(data):
    """Parse TOML bytes and retain a useful diagnostic for the operator."""
    try:
        return tomllib.loads(data.decode("utf-8"))
    except (ValueError, UnicodeError) as exc:
        raise Error(f"invalid TOML: {exc}") from exc


def parse_variant(data):
    """Limit a variant to fully specified lane providers, never agent patches."""
    doc = read_toml(data)
    providers = doc.get("providers")
    if set(doc) != {"providers"} or not isinstance(providers, dict) or not providers:
        raise Error("variant must contain only nonempty [providers.lane_*] tables")
    for name, provider in providers.items():
        if not re.fullmatch(r"lane_[a-z0-9_]+", name) or not isinstance(provider, dict):
            raise Error(f"not a lane provider: {name}")
        if set(provider) - {"base", "option_defaults", "args_append"}:
            raise Error(f"{name}: only base, option_defaults and args_append may vary")
        base = provider.get("base", "")
        if not isinstance(base, str) or not re.fullmatch(r"provider:(?!lane_)[a-zA-Z0-9_-]+", base):
            raise Error(f"{name}: base must select a concrete provider sibling")
        defaults = provider.get("option_defaults", {})
        if (not isinstance(defaults, dict) or set(defaults) != {"model", "effort"}
                or not all(isinstance(v, str) for v in defaults.values()) or not defaults["model"].strip()):
            raise Error(f"{name}: model and effort must both be explicit; model cannot be empty")
        args = provider.get("args_append", [])
        if not isinstance(args, list) or (args and (len(args) != 2 or args[0] != "--profile"
                     or not isinstance(args[1], str) or not re.fullmatch(r"[a-zA-Z0-9_-]+", args[1]))):
            raise Error(f"{name}: args_append may only be [\"--profile\", \"NAME\"]")
    return providers


def check_layout(city):
    """Require the ignored active include and reject write-through symlinks."""
    for relative in (Path("city.toml"), Path(".gc"), ACTIVE, PREVIOUS):
        path = city / relative
        if path.is_symlink():
            raise Error(f"refusing symlink: {path}")
    root = (city / "city.toml").read_bytes()
    document = read_toml(root)
    includes = document.get("include", [])
    if not isinstance(includes, list) or includes.count(str(ACTIVE)) != 1:
        raise Error(f"one-time installation required: include = [\"{ACTIVE}\"] in city.toml")
    for include in [*includes, *document.get("workspace", {}).get("includes", [])]:
        if include == str(ACTIVE):
            continue
        path = Path(include)
        absolute = path if path.is_absolute() else city / path
        if absolute.resolve().is_relative_to((city / ".gc").resolve()):
            raise Error(f"unsupported runtime config composition input: {include}; move it to tracked config")
    if not (city / ACTIVE).is_file():
        raise Error(f"one-time installation required: missing {ACTIVE}")
    if (city / PREVIOUS).exists() and not (city / PREVIOUS).is_file():
        raise Error(f"previous configuration is not a regular file: {PREVIOUS}")
    return root, (city / ACTIVE).read_bytes()


@contextmanager
def replacement_view(city, data):
    """Substitute active bytes in an owned scratch root, never stacked -f."""
    city = Path(city).resolve()
    root, _ = check_layout(city)
    with tempfile.TemporaryDirectory(prefix="anthony-profile-") as directory:
        scratch = Path(directory)
        for entry in city.iterdir():
            if entry.name not in ("city.toml", ".gc"):
                (scratch / entry.name).symlink_to(entry, target_is_directory=entry.is_dir())
        (scratch / "city.toml").write_bytes(root)
        (scratch / ".gc").mkdir()
        # gc config show regenerates .gc/scripts and prunes .gc/system/packs.
        # Never symlink runtime trees. site.toml is an actual config input;
        # explicitly referenced unsupported runtime paths fail validation.
        site = city / ".gc/site.toml"
        if site.exists():
            (scratch / ".gc/site.toml").write_bytes(site.read_bytes())
        (scratch / ACTIVE).write_bytes(data)
        yield scratch


def atomic_write(path, data):
    """Durably replace one owned file using a temporary in the same directory."""
    descriptor, filename = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(filename)
    try:
        with os.fdopen(descriptor, "wb") as stream:
            stream.write(data)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        temporary.unlink(missing_ok=True)


def compare_launch(desired, observed):
    """Compare launch evidence, never hashes or a bead's stored provider."""
    if observed is None:
        return "unverified"
    for key in ("provider", "model", "home", "profile", "effort"):
        if desired.get(key) is not None and observed.get(key) is not None and desired[key] != observed[key]:
            return "pending-cutover"
    required = ["provider", "model", "home", "profile"]
    if desired.get("provider") == "claude":
        required.append("effort")
    if any(desired.get(key) is not None and observed.get(key) is None for key in required):
        return "unverified"
    if observed.get("unsafe_effort_flag"):
        return "pending-cutover"
    # CLI flags and the selected profile do not prove effective settings after
    # project config, resume, or an interactive model/effort change.
    return "launch-matches; effective settings unverified"


def migration_problem(agent):
    """Return the lane invariant violated by an agent, including later drift."""
    for key in ("model", "effort"):
        if key in (agent.get("OptionDefaults") or {}):
            return f"remove agent-level {key}, even if empty, before opting into a lane"
    for key in ("ResumeCommand", "StartCommand", "Args"):
        if agent.get(key):
            return f"managed agent must not override {key}"
    if (agent.get("Env") or {}).get("CODEX_HOME") is not None:
        return "agent CODEX_HOME overrides the account lane"
    return None


def observe_panes(city, herdr_session, run=run_command):
    """Query herdr PIDs and allowlisted ps fields; never return raw environments."""
    observations, problems = [], []
    prefix = ["herdr", "--session", herdr_session, "pane"]
    try:
        payload = json.loads(run([*prefix, "list"]).stdout)
        panes = payload["result"]["panes"]
    except (Error, ValueError, KeyError, TypeError) as exc:
        return [], [f"herdr inventory unverified: {exc}"]
    for pane in panes:
        pane_id = pane.get("pane_id")
        try:
            payload = json.loads(run([*prefix, "process-info", "--pane", pane_id]).stdout)
            processes = payload["result"]["process_info"]["foreground_processes"]
            for process in processes:
                args = process.get("argv") or []
                if not args or Path(args[0]).name not in ("claude", "codex"):
                    continue
                pid = process["pid"]
                if not isinstance(pid, int) or pid <= 0:
                    raise Error("invalid foreground PID")
                raw = run(["ps", "eww", "-p", str(pid), "-o", "command="]).stdout
                allowed = {"GC_CITY", "GC_SESSION_ID", "GC_TEMPLATE", "CODEX_HOME"}
                fields = {}
                for token in raw.split():
                    key, separator, value = token.partition("=")
                    if separator and key in allowed:
                        if key in fields:
                            raise Error(f"ambiguous {key} in PID {pid}; environment not shown")
                        fields[key] = value
                if fields.get("GC_CITY") != str(city):
                    # This includes personal panes in the same herdr session.
                    continue
                if not fields.get("GC_SESSION_ID") or not fields.get("GC_TEMPLATE"):
                    raise Error(f"PID {pid} lacks a session bead ID or template")
                flags = {}
                for index, arg in enumerate(args):
                    for long, short, key in (("--model", "-m", "model"), ("--profile", "-p", "profile"),
                                             ("--effort", "--effort", "effort")):
                        value = None
                        if arg in (long, short) and index + 1 < len(args):
                            value = args[index + 1]
                        elif arg.startswith(long + "="):
                            value = arg[len(long) + 1:]
                        if value is not None:
                            if key in flags or not re.fullmatch(r"[a-zA-Z0-9_.:/-]+", value):
                                raise Error(f"PID {pid}: ambiguous or unsupported {key} flag")
                            flags[key] = value
                observations.append({"pane": pane_id, "pid": pid,
                                     "session_id": fields["GC_SESSION_ID"], "template": fields["GC_TEMPLATE"],
                                     "provider": Path(args[0]).name, "home": fields.get("CODEX_HOME"),
                                     "model": flags.get("model"), "profile": flags.get("profile"),
                                     "effort": flags.get("effort"),
                                     "unsafe_effort_flag": any("model_reasoning_effort=" in arg for arg in args)})
        except (Error, ValueError, KeyError, TypeError, IndexError) as exc:
            problems.append(f"pane {pane_id}: {exc}")
    return observations, problems


class Switcher:
    """One operator's config transaction; runtime state is never persisted here."""

    def __init__(self, city, gc="gc", run=run_command):
        self.city = Path(city).resolve()
        self.gc = gc
        self.run = run
        self.warnings = []

    def command(self, city, *args):
        result = self.run([self.gc, "--city", str(city), *args])
        if result.stderr.strip():
            self.warnings.append(result.stderr.strip())
        return result.stdout

    def json_command(self, city, *args):
        try:
            result = json.loads(self.command(city, *args))
        except ValueError as exc:
            raise Error(f"invalid JSON from gc {' '.join(args)}: {exc}") from exc
        if not isinstance(result, dict) or result.get("ok") is False:
            raise Error(f"gc {' '.join(args)} did not return a successful object")
        self.warnings.extend(str(warning) for warning in (result.get("warnings") or []))
        self.warnings.extend(str(warning) for warning in (result.get("validation", {}).get("warnings") or []))
        return result

    def load_config(self, city):
        config = self.json_command(city, "config", "show", "--validate", "--json")["config"]
        # This deployed JSON omits BindingName. Explain emits canonical names in
        # the same config order, unlike agent list which may query accepted state.
        names = re.findall(r"^Agent: (.+)$", self.command(city, "config", "explain"), re.MULTILINE)
        agents = config["Agents"]
        if len(names) != len(agents):
            raise Error("config explain names do not match config show; cannot identify managed templates")
        for agent, name in zip(agents, names):
            directory, _, base = name.rpartition("/")
            if directory != agent.get("Dir", "") or base.split(".")[-1] != agent["Name"]:
                raise Error(f"config explain order changed at {name}; cannot identify managed templates")
            agent["identity"] = name
        for warning in self.warnings:
            if re.search(r"\bskipping\b|not found|failed to (?:load|read)|cannot (?:load|read)", warning, re.I):
                raise Error(f"incomplete config composition: {warning}")
        return config

    def lane_settings(self, city, name, provider):
        explained = self.json_command(city, "config", "explain", "--provider", name, "--json")
        ancestor = explained["builtin_ancestor"]
        resolved = explained["resolved"]
        defaults = provider["option_defaults"]
        if any(resolved["option_defaults"].get(k) != v for k, v in defaults.items()):
            raise Error(f"{name}: resolved defaults differ from candidate")
        args = resolved.get("args") or []
        if args != provider.get("args_append", []):
            raise Error(f"{name}: inherited args conflict with lane-owned launch arguments")
        settings = {"provider": ancestor, "base": provider["base"], **defaults}
        if ancestor == "codex":
            if defaults["effort"] != "":
                raise Error(f"{name}: Codex effort must be empty to avoid herdr's '=' launch bug")
            if defaults["model"].startswith("claude-"):
                raise Error(f"{name}: Claude model on a Codex lane")
            home = (resolved.get("env") or {}).get("CODEX_HOME", "")
            if not home or not Path(home).is_absolute():
                raise Error(f"{name}: Codex account sibling must set absolute CODEX_HOME")
            if not args:
                raise Error(f"{name}: Codex requires an explicit effort-only --profile; no home-default fallback")
            profile = Path(home) / f"{args[1]}.config.toml"
            try:
                policy = read_toml(profile.read_bytes())
            except OSError as exc:
                raise Error(f"{name}: cannot read effort profile {profile}: {exc}") from exc
            if set(policy) != {"model_reasoning_effort"} or policy["model_reasoning_effort"] not in (
                    "minimal", "low", "medium", "high", "xhigh"):
                raise Error(f"{name}: profile must be effort-only with an explicit supported effort")
            settings.update(home=home, profile=args[1], effort=policy["model_reasoning_effort"])
        elif ancestor == "claude":
            if not defaults["effort"] or defaults["model"].startswith("gpt-") or args:
                raise Error(f"{name}: Claude needs explicit model/effort and no Codex profile")
        else:
            raise Error(f"{name}: unsupported lane ancestor {ancestor}; expected claude or codex")
        return settings

    def preview(self, data):
        """Validate replacement bytes and report a small diff plus lane membership."""
        self.warnings = []
        providers = parse_variant(data)
        _, original = check_layout(self.city)
        with replacement_view(self.city, data) as scratch:
            config = self.load_config(scratch)
            loaded = config["Providers"]
            for name, provider in providers.items():
                actual = loaded.get(name, {})
                if (actual.get("Base") != provider["base"]
                        or actual.get("OptionDefaults") != provider["option_defaults"]
                        or (actual.get("ArgsAppend") or []) != provider.get("args_append", [])):
                    raise Error(f"{name}: loaded candidate differs; duplicate lane definition or unfaithful replacement")
            lanes = {name: self.lane_settings(scratch, name, provider) for name, provider in providers.items()}
            for name, provider in loaded.items():
                base = (provider.get("Base") or "").strip().removeprefix("provider:")
                if name not in providers and base.startswith("lane_"):
                    raise Error(f"{name}: concrete providers must not inherit lanes; this would switch an exception")
            if config.get("Workspace", {}).get("Provider", "").startswith("lane_"):
                raise Error("workspace default cannot be a lane; membership must be explicit on agents")
            managed, unmanaged = [], []
            for agent in config["Agents"]:
                name = agent["identity"]
                provider = agent.get("Provider", "")
                if not provider.startswith("lane_"):
                    unmanaged.append(name)
                    continue
                if provider not in lanes:
                    raise Error(f"{name}: missing lane {provider} in candidate")
                problem = migration_problem(agent)
                if problem:
                    raise Error(f"{name}: {problem}")
                managed.append(name)
        diff = "".join(difflib.unified_diff(original.decode().splitlines(True), data.decode().splitlines(True),
                                            fromfile=str(ACTIVE), tofile="candidate"))
        return {"diff": diff, "managed": sorted(managed), "unmanaged": sorted(unmanaged),
                "lanes": lanes, "warnings": list(dict.fromkeys(self.warnings))}

    def apply(self, data, *, save_previous=True):
        """Validate, preserve old bytes, activate, and request only a soft reload."""
        root, original = check_layout(self.city)
        preview = self.preview(data)
        if ((self.city / "city.toml").read_bytes(), (self.city / ACTIVE).read_bytes()) != (root, original):
            raise Error("city.toml or active lane file changed during validation; preview again")
        check_layout(self.city)
        changed = data != original
        if changed:
            if save_previous:
                atomic_write(self.city / PREVIOUS, original)
            try:
                atomic_write(self.city / ACTIVE, data)
            except OSError as exc:
                # A directory fsync can fail after rename: never assert unchanged.
                return {**preview, "reload": "unconfirmed", "diagnostic": f"activation uncertain: {exc}; inspect active bytes", "changed": True}
        try:
            reply = self.run([self.gc, "--city", str(self.city), "reload", "--soft", "--timeout", "45s"], timeout=55)
            diagnostic = "\n".join(part.strip() for part in (reply.stdout, reply.stderr) if part.strip())
            uncertain = bool(reply.stderr.strip()) or bool(re.search(r"warning|may still drain|desired state is empty", diagnostic, re.I))
            return {**preview, "reload": "unconfirmed" if uncertain else "confirmed", "diagnostic": diagnostic, "changed": changed}
        except Error as exc:
            return {**preview, "reload": "unconfirmed", "diagnostic": str(exc), "changed": changed}

    def undo(self):
        """Restore saved desired bytes through the same validation and apply path."""
        check_layout(self.city)
        try:
            data = (self.city / PREVIOUS).read_bytes()
        except OSError as exc:
            raise Error(f"no readable previous configuration: {exc}") from exc
        # Undo is an idempotent restore, not a two-file toggle. Keeping its
        # recovery target intact makes an interrupted undo safely retryable.
        return self.apply(data, save_previous=False)

    def status(self, herdr_session=None):
        """Read desired settings, session beads and real launch observations anew."""
        self.warnings = []
        config = self.load_config(self.city)
        lanes = {}
        for name, provider in config["Providers"].items():
            if name.startswith("lane_"):
                lanes[name] = self.lane_settings(self.city, name, {
                    "base": provider["Base"], "option_defaults": provider["OptionDefaults"],
                    "args_append": provider.get("ArgsAppend") or []})
        templates = {agent["identity"]: agent.get("Provider", "") for agent in config["Agents"]}
        sessions = self.json_command(self.city, "session", "list", "--json")["sessions"]
        observed, problems = observe_panes(self.city, herdr_session or self.city.name, self.run)
        invalid = {agent["identity"]: migration_problem(agent) for agent in config["Agents"]
                   if agent.get("Provider", "").startswith("lane_") and migration_problem(agent)}
        problems.extend(f"{identity}: {problem}" for identity, problem in invalid.items())
        seats = []
        known_ids = set()
        for session in sessions:
            if session.get("closed"):
                continue
            identity = session["id"]
            known_ids.add(identity)
            template = session.get("template", "")
            provider = templates.get(template)
            desired = lanes.get(provider)
            evidence = [item for item in observed if item["session_id"] == identity]
            launch = evidence[0] if len(evidence) == 1 else None
            if launch and launch["template"] != template:
                problems.append(f"{identity}: process template and bead template disagree")
                launch = None
            if len(evidence) > 1:
                problems.append(f"{identity}: multiple provider processes; no adoption verdict")
            verdict = compare_launch(desired, launch) if desired else "unmanaged / preserved"
            if template in invalid:
                verdict = "unverified"
                desired = None
            if provider is None:
                verdict = "unverified"
                problems.append(f"{identity}: template {template} is absent from current configuration")
            if not evidence and session.get("state") == "active":
                problems.append(f"{identity}: active bead has no correlated live process; not proof it stopped")
            seats.append({"id": identity, "name": session.get("name"), "template": template,
                          "lifecycle": session.get("state"), "desired_lane": provider if desired else None,
                          "desired": desired, "observed": evidence, "verdict": verdict})
        unlinked = [item for item in observed if item["session_id"] not in known_ids]
        if unlinked:
            problems.append("live city processes have no open session bead in this snapshot")
        return {"city": str(self.city), "lanes": lanes, "seats": seats, "unlinked_processes": unlinked,
                "problems": problems, "warnings": list(dict.fromkeys(self.warnings)),
                "note": "Read-only snapshots are not atomic. Launch matches do not prove effective model/effort or controller acceptance."}


def main(argv=None):
    """Expose the deliberate operator contract; no implicit install or migration."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--city", required=True, type=Path)
    parser.add_argument("--gc", default="gc", help="deployed gc executable")
    commands = parser.add_subparsers(dest="action", required=True)
    for action in ("preview", "apply"):
        sub = commands.add_parser(action)
        sub.add_argument("variant", help="path, or name within CITY/config/lanes/")
    commands.add_parser("undo")
    status = commands.add_parser("status")
    status.add_argument("--json", action="store_true")
    status.add_argument("--herdr-session", help="defaults to the city directory name")
    args = parser.parse_args(argv)
    switcher = Switcher(args.city, gc=args.gc)
    try:
        if args.action == "status":
            result = switcher.status(args.herdr_session)
            if args.json:
                print(json.dumps(result, indent=2))
            else:
                for seat in result["seats"]:
                    print(f"{seat['id']}  {seat['name']}  bead={seat['lifecycle']}  {seat['verdict']}")
                    if seat["desired"]:
                        print(f"  desired {json.dumps(seat['desired'], sort_keys=True)}")
                    for observed in seat["observed"]:
                        print(f"  observed {json.dumps(observed, sort_keys=True)}")
                for observed in result["unlinked_processes"]:
                    print(f"Unlinked process: {json.dumps(observed, sort_keys=True)}")
                for warning in result["warnings"] + result["problems"]:
                    print(warning, file=sys.stderr)
                print(result["note"])
            return 2 if result["problems"] or any(seat["verdict"] == "unverified" for seat in result["seats"]) else 0
        if args.action == "undo":
            result = switcher.undo()
        else:
            path = Path(args.variant)
            if not path.is_file():
                path = args.city / "config/lanes" / f"{args.variant}.toml"
            data = path.read_bytes()
            result = getattr(switcher, args.action)(data)
        print(result["diff"] or "Active lane bytes already match.")
        for lane, settings in result["lanes"].items():
            print(f"{lane}: {json.dumps(settings, sort_keys=True)}")
        print("Managed templates: " + (", ".join(result["managed"]) or "none"))
        print("Unmanaged / preserved: " + (", ".join(result["unmanaged"]) or "none"))
        for warning in result["warnings"]:
            print(warning, file=sys.stderr)
        if "reload" in result:
            print(f"Desired configuration on disk; soft reload {result['reload']}. Running seats have NOT been migrated.")
            print(result["diagnostic"])
            print("Inspect status; move seats manually at safe stopping points. Undo restores desired bytes, not sessions.")
            return 0 if result["reload"] == "confirmed" else 2
        return 0
    except (Error, OSError, KeyError, TypeError) as exc:
        print(f"anthony-profile: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
