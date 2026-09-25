"""Small, validated configuration for one worker and its acceptance checks."""

import tomllib
from pathlib import Path
from typing import Literal

from pydantic import BaseModel, ConfigDict, model_validator


class Model(BaseModel):
    model_config = ConfigDict(extra="forbid", strict=True)


class Stage(Model):
    prompt: str
    pre: list[str] = []
    cwd: str = "."
    checks: list[str] = []
    tools: list[str] = ["Read", "Glob", "Grep", "Write", "Edit", "Bash"]


class Check(Model):
    command: str | None = None
    prompt: str | None = None
    tools: list[str] = ["Read", "Glob", "Grep", "Bash", "Skill", "Agent"]

    @model_validator(mode="after")
    def one_source(self):
        if (self.command is not None) and (self.prompt is not None):
            raise ValueError("A check needs exactly one of command or prompt")
        value = self.command if self.command is not None else self.prompt
        if value is None or not value.strip():
            raise ValueError("A check needs exactly one nonempty command or prompt")
        return self


class Config(Model):
    stages: dict[str, Stage]
    checks: dict[str, Check] = {}

    @classmethod
    def load(cls, path: Path):
        return cls.model_validate(tomllib.loads(path.read_text()))


class CheckResult(Model):
    status: Literal["pass", "retry", "stop"]
    feedback: str

    @model_validator(mode="after")
    def failure_has_feedback(self):
        if self.status != "pass" and not self.feedback.strip():
            raise ValueError("An unsuccessful check must explain why")
        return self
