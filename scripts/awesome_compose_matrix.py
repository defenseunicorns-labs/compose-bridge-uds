#!/usr/bin/env -S uv run --script
#
# /// script
# requires-python = ">=3.9"
# dependencies = []
# ///

import json
import re
import subprocess
import sys
import tempfile
from pathlib import Path

AWESOME_REF = "30f4b7f6a6c3b0c0ecf4d4efb0de203c48d11562"
AWESOME_REPOSITORY = "https://github.com/docker/awesome-compose.git"
COMPOSE_FILENAMES = (
    "compose.yaml",
    "compose.yml",
    "docker-compose.yaml",
    "docker-compose.yml",
)
ISSUE_PATTERN = re.compile(r"^- \[([^]]+)] ([^:]+):", re.MULTILINE)
BASELINE_START = "<!-- matrix-baseline:start -->"
BASELINE_END = "<!-- matrix-baseline:end -->"
RESULTS_START = "<!-- matrix-results:start -->"
RESULTS_END = "<!-- matrix-results:end -->"


def compose_files(awesome_dir: Path):
    for sample_dir in sorted(path for path in awesome_dir.iterdir() if path.is_dir()):
        compose_file = next(
            (
                sample_dir / name
                for name in COMPOSE_FILENAMES
                if (sample_dir / name).is_file()
            ),
            None,
        )
        if compose_file:
            yield sample_dir.name, compose_file


def canonical_model(compose_file: Path):
    result = subprocess.run(
        [
            "docker",
            "compose",
            "--ansi",
            "never",
            "-f",
            compose_file.name,
            "config",
            "--format",
            "json",
        ],
        check=False,
        cwd=compose_file.parent,
        capture_output=True,
        text=True,
    )
    if result.returncode:
        raise RuntimeError(result.stderr.strip())

    model = json.loads(result.stdout)
    for name, service in model["services"].items():
        if "build" in service and not service.get("image"):
            service["image"] = f"{model['name']}-{name}"
    return model


def command_output(command, cwd=None):
    return subprocess.run(
        command,
        check=True,
        cwd=cwd,
        capture_output=True,
        text=True,
    ).stdout.strip()


def tested_baseline(repo_root: Path):
    bridge_commit = command_output(["git", "rev-parse", "HEAD"], cwd=repo_root)
    if command_output(["git", "status", "--short"], cwd=repo_root):
        bridge_commit += "-dirty"

    awesome_commit = (
        f"[`{AWESOME_REF}`]"
        f"(https://github.com/docker/awesome-compose/commit/{AWESOME_REF})"
    )
    return [
        ("Compose Bridge", f"`{bridge_commit}`"),
        ("Awesome Compose", awesome_commit),
        (
            "Docker Compose",
            f"`{command_output(['docker', 'compose', 'version', '--short'])}`",
        ),
        ("Go", f"`{command_output(['go', 'version']).removeprefix('go version ')}`"),
    ]


def replace_generated_section(document, start_marker, end_marker, content):
    content_start = document.index(start_marker) + len(start_marker)
    content_end = document.index(end_marker, content_start)
    return (
        document[:content_start]
        + "\n"
        + content.rstrip()
        + "\n"
        + document[content_end:]
    )


def update_matrix_document(matrix_file: Path, results, baseline):
    baseline_lines = ["| Input | Value |", "|---|---|"]
    baseline_lines.extend(f"| {name} | {value} |" for name, value in baseline)

    supported = sum(result[1] == "Supported" for result in results)
    rejected = sum(result[1] == "Rejected" for result in results)
    summary = (
        f"All {len(results)} files canonicalized. The bridge supported and "
        f"rendered {supported} models and rejected {rejected} with diagnostics."
    )
    baseline_lines.extend(["", summary])

    result_lines = [
        "| Sample | Static result | Diagnostics |",
        "|---|---|---|",
    ]
    for sample, status, diagnostics in results:
        rendered = ", ".join(f"`{diagnostic}`" for diagnostic in diagnostics)
        result_lines.append(f"| `{sample}` | {status} | {rendered} |")

    document = matrix_file.read_text()
    document = replace_generated_section(
        document, BASELINE_START, BASELINE_END, "\n".join(baseline_lines)
    )
    document = replace_generated_section(
        document, RESULTS_START, RESULTS_END, "\n".join(result_lines)
    )
    matrix_file.write_text(document)


def main():
    repo_root = Path(__file__).resolve().parents[1]
    results = []

    with tempfile.TemporaryDirectory(prefix="awesome-compose-matrix-") as temp:
        work_dir = Path(temp)
        awesome_dir = work_dir / "awesome-compose"
        bridge = work_dir / "compose-bridge-uds"

        subprocess.run(
            ["git", "clone", "--quiet", AWESOME_REPOSITORY, awesome_dir], check=True
        )
        subprocess.run(
            ["git", "-C", awesome_dir, "checkout", "--quiet", AWESOME_REF], check=True
        )
        subprocess.run(["go", "build", "-o", bridge, "."], cwd=repo_root, check=True)

        for sample, compose_file in compose_files(awesome_dir):
            try:
                model = canonical_model(compose_file)
            except (json.JSONDecodeError, RuntimeError) as error:
                print(f"{sample}: {error}", file=sys.stderr)
                results.append((sample, "Failed", ["compose-config"]))
                continue

            sample_dir = work_dir / "results" / sample
            sample_dir.mkdir(parents=True)
            canonical_file = sample_dir / "compose.json"
            canonical_file.write_text(json.dumps(model, indent=2) + "\n")
            result = subprocess.run(
                [bridge, "-in", canonical_file, "-out", sample_dir / "out"],
                capture_output=True,
                text=True,
                check=False,
            )

            if result.returncode == 0:
                results.append((sample, "Supported", []))
                continue

            diagnostics = sorted(
                {
                    f"{code}: {path}"
                    for code, path in ISSUE_PATTERN.findall(result.stderr)
                }
            )
            if diagnostics:
                results.append((sample, "Rejected", diagnostics))
            else:
                print(f"{sample}: {result.stderr.strip()}", file=sys.stderr)
                results.append((sample, "Failed", ["transform-error"]))

    for status in ("Supported", "Rejected", "Failed"):
        print(f"{status}: {sum(result[1] == status for result in results)}")

    if any(result[1] == "Failed" for result in results):
        print(
            "Results table not updated because the matrix had failures.",
            file=sys.stderr,
        )
        return 1

    matrix_file = repo_root / "docs" / "awesome-compose-compatibility-matrix.md"
    update_matrix_document(matrix_file, results, tested_baseline(repo_root))
    print(f"Updated: {matrix_file}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.CalledProcessError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
