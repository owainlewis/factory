import sys
from importlib.metadata import version

import pytest

from factory import cli


def test_version_flag_prints_distribution_version_and_exits_zero(capsys, monkeypatch):
    monkeypatch.setattr(sys, "argv", ["factory", "--version"])

    with pytest.raises(SystemExit) as excinfo:
        cli.main()

    assert excinfo.value.code == 0
    out = capsys.readouterr().out
    assert version("factory-cli") in out


def test_version_flag_does_not_prompt_for_task(monkeypatch, capsys):
    monkeypatch.setattr(sys, "argv", ["factory", "--version"])

    def fail_input(prompt=""):
        raise AssertionError("must not prompt for a task when printing the version")

    monkeypatch.setattr("builtins.input", fail_input)

    with pytest.raises(SystemExit) as excinfo:
        cli.main()

    assert excinfo.value.code == 0


def test_version_flag_does_not_require_claude_executable(monkeypatch, capsys):
    monkeypatch.setattr(sys, "argv", ["factory", "--version"])
    monkeypatch.setattr("shutil.which", lambda name: None)

    with pytest.raises(SystemExit) as excinfo:
        cli.main()

    assert excinfo.value.code == 0


def test_version_flag_does_not_require_config_file(monkeypatch, tmp_path, capsys):
    # No .factory/config.toml exists under tmp_path; --version must still succeed.
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(sys, "argv", ["factory", "--version"])

    with pytest.raises(SystemExit) as excinfo:
        cli.main()

    assert excinfo.value.code == 0


def test_version_flag_ignores_extraneous_positional_task(monkeypatch, capsys):
    # --version should short-circuit even when other arguments are present.
    monkeypatch.setattr(sys, "argv", ["factory", "--version", "some task"])

    with pytest.raises(SystemExit) as excinfo:
        cli.main()

    assert excinfo.value.code == 0
    assert version("factory-cli") in capsys.readouterr().out


def test_parser_still_accepts_normal_task_argument():
    args = cli.parser().parse_args(["Do the thing"])
    assert args.task == "Do the thing"
    assert args.stage == "build"
