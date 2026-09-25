import pytest
from pydantic import ValidationError

from factory.config import Check


def test_check_requires_a_source():
    with pytest.raises(ValidationError):
        Check.model_validate({})


@pytest.mark.parametrize(
    "data",
    [
        {"command": "true", "prompt": "a.md"},
        {"command": "", "prompt": "a.md"},
        {"command": "true", "prompt": ""},
        {"command": "", "prompt": ""},
    ],
)
def test_check_rejects_both_fields_even_if_one_is_empty(data):
    with pytest.raises(ValidationError):
        Check.model_validate(data)


@pytest.mark.parametrize("field", ["command", "prompt"])
@pytest.mark.parametrize("value", ["", "   ", "\t\n"])
def test_check_rejects_whitespace_only_value(field, value):
    with pytest.raises(ValidationError):
        Check.model_validate({field: value})


def test_check_preserves_command_content_without_stripping():
    check = Check.model_validate({"command": "  echo hello  "})
    assert check.command == "  echo hello  "


def test_check_accepts_a_single_nonempty_command():
    check = Check.model_validate({"command": "pytest"})
    assert check.command == "pytest"
    assert check.prompt is None


def test_check_accepts_a_single_nonempty_prompt():
    check = Check.model_validate({"prompt": "CHECK.md"})
    assert check.prompt == "CHECK.md"
    assert check.command is None


@pytest.mark.parametrize(
    "data",
    [
        {"command": "pytest"},
        {"prompt": "CHECK.md"},
    ],
)
def test_check_round_trips_through_model_dump(data):
    check = Check.model_validate(data)
    dumped = check.model_dump()
    reloaded = Check.model_validate(dumped)
    assert reloaded == check
