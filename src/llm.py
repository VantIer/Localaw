import json
import re
from typing import Any, Dict, List, Optional

from openai import OpenAI


class CommandParser:
    PATTERN = re.compile(r"```(?:json)?\s*\n?(.*?)\n?```", re.DOTALL)

    @staticmethod
    def parse(text: str) -> Optional[List[Dict[str, Any]]]:
        matches = CommandParser.PATTERN.findall(text)
        commands = []
        for match in matches:
            try:
                cmd = json.loads(match.strip())
                if isinstance(cmd, dict) and "action" in cmd:
                    commands.append(cmd)
                elif isinstance(cmd, list):
                    for c in cmd:
                        if isinstance(c, dict) and "action" in c:
                            commands.append(c)
            except json.JSONDecodeError:
                continue
        return commands if commands else None


class LLMClient:
    def __init__(self, api_base: str, api_key: str):
        self.client = OpenAI(base_url=api_base, api_key=api_key)

    def chat(
        self, messages: list, model: str, temperature: float = 0.7, stream: bool = False
    ):
        return self.client.chat.completions.create(
            model=model, messages=messages, temperature=temperature, stream=stream
        )
