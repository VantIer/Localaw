import json
import re
from typing import List, Dict, Any, Optional
from openai import OpenAI


class Command:
    LIST_DIR = "list_dir"
    READ_FILE = "read_file"
    DELETE_FILE = "delete_file"
    WRITE_FILE = "write_file"
    EXEC_CMD = "exec_cmd"


class CommandParser:
    PATTERN = re.compile(
        r'```(?:json)?\s*\n?(.*?)\n?```',
        re.DOTALL
    )

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
    def __init__(self, config):
        self.config = config
        self.client = OpenAI(
            base_url=config.api_base,
            api_key=config.api_key
        )
        self.conversation_history: List[Dict[str, str]] = []

    def set_system_prompt(self, prompt: str):
        self.config.system_prompt = prompt

    def send_message(self, message: str) -> str:
        self.conversation_history.append({"role": "user", "content": message})
        
        messages = [{"role": "system", "content": self.config.system_prompt}]
        messages.extend(self.conversation_history)

        try:
            response = self.client.chat.completions.create(
                model=self.config.model,
                messages=messages,
                temperature=0.7
            )
            
            assistant_message = response.choices[0].message.content
            if assistant_message is None:
                assistant_message = ""
            self.conversation_history.append({"role": "assistant", "content": assistant_message})
            return assistant_message
        except Exception as e:
            self.conversation_history.pop()
            raise Exception(f"LLM API error: {str(e)}")

    def reset_conversation(self):
        self.conversation_history = []
