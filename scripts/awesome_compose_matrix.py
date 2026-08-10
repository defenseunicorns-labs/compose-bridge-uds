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


def update_results_table(matrix_file: Path, results):
    lines = [
        "| Sample | Static result | Diagnostics |",
        "|---|---|---|",
    ]
    for sample, status, diagnostics in results:
        rendered = ", ".join(f"`{diagnostic}`" for diagnostic in diagnostics)
        lines.append(f"| `{sample}` | {status} | {rendered} |")

    document = matrix_file.read_text()
    table_start = document.index("| Sample | Static result | Diagnostics |")
    table_end = document.index("\n\n## ", table_start)
    matrix_file.write_text(
        document[:table_start] + "\n".join(lines) + document[table_end:]
    )


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
    update_results_table(matrix_file, results)
    print(f"Updated: {matrix_file}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, subprocess.CalledProcessError) as error:
        print(f"error: {error}", file=sys.stderr)
        raise SystemExit(1)
