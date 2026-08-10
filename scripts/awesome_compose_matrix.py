#!/usr/bin/env python3

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile


DEFAULT_AWESOME_REF = "30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562"
AWESOME_REPOSITORY = "https://github.com/docker/awesome-compose.git"
COMPOSE_FILENAMES = (
    "compose.yaml",
    "compose.yml",
    "docker-compose.yaml",
    "docker-compose.yml",
)
ISSUE_PATTERN = re.compile(r"^- \[([^]]+)] ([^:]+):", re.MULTILINE)
SERVICE_NETWORK_FIELDS = {
    "aliases",
    "driver_opts",
    "interface_name",
    "ipv4_address",
    "ipv6_address",
    "link_local_ips",
    "mac_address",
}
TOP_LEVEL_NETWORK_FIELDS = {
    "driver",
    "driver_opts",
    "internal",
    "attachable",
    "ipam",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Run the Compose Bridge static matrix against Awesome Compose."
    )
    parser.add_argument(
        "--awesome-ref",
        default=DEFAULT_AWESOME_REF,
        help="Awesome Compose commit or ref to test.",
    )
    parser.add_argument(
        "--report",
        type=Path,
        default=Path("/tmp/awesome-compose-report.md"),
        help="Markdown report path.",
    )
    return parser.parse_args()


def run(command: list[str], **kwargs: object) -> subprocess.CompletedProcess[str]:
    return subprocess.run(command, check=False, text=True, **kwargs)


def run_required(command: list[str], **kwargs: object) -> None:
    result = run(command, **kwargs)
    if result.returncode != 0:
        raise RuntimeError(f"command failed ({result.returncode}): {' '.join(command)}")


def find_compose_files(awesome_dir: Path) -> list[tuple[str, Path]]:
    selected: dict[Path, Path] = {}
    for filename in COMPOSE_FILENAMES:
        for path in awesome_dir.rglob(filename):
            if ".git" in path.parts:
                continue
            selected.setdefault(path.parent, path)

    return sorted(
        (
            (directory.relative_to(awesome_dir).as_posix(), path)
            for directory, path in selected.items()
        ),
        key=lambda item: item[0],
    )


def canonical_model(compose_file: Path) -> tuple[dict[str, object] | None, str]:
    environment = os.environ.copy()
    environment["COMPOSE_ANSI"] = "never"
    result = run(
        [
            "docker",
            "compose",
            "-f",
            compose_file.name,
            "config",
            "--format",
            "json",
        ],
        cwd=compose_file.parent,
        env=environment,
        capture_output=True,
    )
    if result.returncode != 0:
        return None, result.stderr

    try:
        model = json.loads(result.stdout)
    except json.JSONDecodeError as error:
        return None, f"decode canonical model: {error}"

    project_name = model.get("name", "")
    for service_name, service in model.get("services", {}).items():
        if service.get("build") is not None and not service.get("image"):
            service["image"] = f"{project_name}-{service_name}"
    return model, ""


def present(value: object) -> bool:
    return value not in (None, "", False, [], {})


def network_labels(model: dict[str, object], path: str) -> set[str]:
    labels: set[str] = set()
    if path.startswith("services.") and ".networks." in path:
        service_path, network_name = path.removeprefix("services.").split(
            ".networks.", 1
        )
        network = (
            model.get("services", {})
            .get(service_path, {})
            .get("networks", {})
            .get(network_name, {})
            or {}
        )
        for key in SERVICE_NETWORK_FIELDS:
            if present(network.get(key)):
                labels.add(f"network.{key}")
    elif path.startswith("networks."):
        network_name = path.removeprefix("networks.")
        network = model.get("networks", {}).get(network_name, {}) or {}
        for key in TOP_LEVEL_NETWORK_FIELDS:
            if present(network.get(key)):
                labels.add(f"network.{key}")
    return labels


def diagnostic_labels(model: dict[str, object], stderr: str) -> list[str]:
    labels: set[str] = set()
    for code, path in ISSUE_PATTERN.findall(stderr):
        if code == "container-name-alias":
            labels.add("container_name")
        elif code == "service-field":
            labels.add(path.rsplit(".", 1)[-1])
        elif code == "network-options":
            labels.update(network_labels(model, path))
        else:
            labels.add(code)
    return sorted(labels)


def render_report(results: list[tuple[str, str, list[str]]]) -> str:
    lines = [
        "# Awesome Compose compatibility report",
        "",
        "| Sample | Static result | Diagnostics |",
        "|---|---|---|",
    ]
    for sample, result, diagnostics in results:
        rendered = ", ".join(f"`{item}`" for item in diagnostics)
        lines.append(f"| `{sample}` | {result} | {rendered} |")
    return "\n".join(lines) + "\n"


def main() -> int:
    args = parse_args()
    repo_root = Path(__file__).resolve().parents[1]
    counts = {"Supported": 0, "Rejected": 0, "Failed": 0}
    results: list[tuple[str, str, list[str]]] = []

    with tempfile.TemporaryDirectory(prefix="awesome-compose-matrix-") as temp:
        work_dir = Path(temp)
        awesome_dir = work_dir / "awesome-compose"
        bridge_binary = work_dir / "compose-bridge-uds"

        run_required(
            [
                "git",
                "clone",
                "--quiet",
                "--no-checkout",
                AWESOME_REPOSITORY,
                str(awesome_dir),
            ]
        )
        run_required(
            ["git", "-C", str(awesome_dir), "checkout", "--quiet", args.awesome_ref]
        )
        run_required(
            ["go", "build", "-o", str(bridge_binary), "."], cwd=repo_root
        )

        for sample, compose_file in find_compose_files(awesome_dir):
            model, error = canonical_model(compose_file)
            if model is None:
                results.append((sample, "Failed", ["compose-config"]))
                counts["Failed"] += 1
                print(f"{sample}: {error.strip()}", file=sys.stderr)
                continue

            sample_work = work_dir / "results" / sample.replace("/", "-")
            output_dir = sample_work / "out"
            sample_work.mkdir(parents=True)
            canonical_file = sample_work / "compose.json"
            canonical_file.write_text(json.dumps(model, indent=2) + "\n")

            result = run(
                [
                    str(bridge_binary),
                    "-in",
                    str(canonical_file),
                    "-out",
                    str(output_dir),
                ],
                capture_output=True,
            )
            if result.returncode == 0:
                status = "Supported"
                diagnostics: list[str] = []
            else:
                diagnostics = diagnostic_labels(model, result.stderr)
                if diagnostics:
                    status = "Rejected"
                else:
                    status = "Failed"
                    diagnostics = ["transform-error"]
                    print(f"{sample}: {result.stderr.strip()}", file=sys.stderr)

            counts[status] += 1
            results.append((sample, status, diagnostics))

    report = render_report(results)
    args.report.parent.mkdir(parents=True, exist_ok=True)
    args.report.write_text(report)
    print(report, end="")
    print(
        f"Supported: {counts['Supported']}; rejected: {counts['Rejected']}; "
        f"failed: {counts['Failed']}"
    )
    print(f"Report: {args.report}")
    return 1 if counts["Failed"] else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (FileNotFoundError, RuntimeError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
