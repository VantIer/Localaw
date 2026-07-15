import json
import re
from typing import Any, Dict, List, Tuple

from openai import OpenAI


class CommandParser:
    PATTERN = re.compile(r"```json\s*\n?(.*?)\n?```", re.DOTALL)

    # JSON 合法转义字符
    _VALID_ESCAPES = set('"\\/bfnrtu')

    @staticmethod
    def _repair_json(json_str: str) -> str:
        """修复 JSON 字符串中常见的转义问题：
        1. 字符串值内未转义的双引号（导致字符串异常闭合）
        2. 非法的反斜杠转义序列（如 \\*、\\x）
        """
        result = []
        i = 0
        n = len(json_str)
        in_string = False

        while i < n:
            ch = json_str[i]

            if not in_string:
                result.append(ch)
                if ch == '"':
                    in_string = True
                i += 1
                continue

            # in_string == True
            if ch == '\\':
                # 检查下一个字符是否为合法 JSON 转义符
                if i + 1 < n and json_str[i + 1] in CommandParser._VALID_ESCAPES:
                    # 合法转义，原样保留并跳过下一字符
                    result.append(ch)
                    result.append(json_str[i + 1])
                    i += 2
                else:
                    # 非法转义（如 \* \x），将 \ 转义为 \\
                    result.append('\\\\')
                    i += 1
                continue

            if ch == '"':
                # 前瞻检查：跳过空白后看下一个字符是否为 JSON 结构字符
                j = i + 1
                while j < n and json_str[j] in ' \t\n\r':
                    j += 1
                is_closing = False
                if j >= n:
                    # 到达字符串末尾，判定为闭合引号
                    is_closing = True
                elif json_str[j] in '}]':
                    # 跟随 } 或 ] → 闭合引号（对象/数组结束）
                    is_closing = True
                elif json_str[j] == ':':
                    # 跟随 : → 闭合引号（key 结束）
                    is_closing = True
                elif json_str[j] == ',':
                    # 跟随 , → 需进一步检查逗号后是否为下一个 key 的引号
                    k = j + 1
                    while k < n and json_str[k] in ' \t\n\r':
                        k += 1
                    if k < n and json_str[k] == '"':
                        # , 后面是 " → 合法的闭合引号（下一 key-value 对）
                        is_closing = True
                    # 否则判定为未转义的内容双引号
                if is_closing:
                    result.append(ch)
                    in_string = False
                else:
                    # 判定为未转义的内容双引号，转义为 \"
                    result.append('\\"')
                i += 1
                continue

            result.append(ch)
            i += 1

        return ''.join(result)

    @staticmethod
    def parse(text: str) -> Tuple[List[Dict[str, Any]], List[str]]:
        """解析文本中的 JSON 命令块。
        返回 (commands, errors)：
        - commands: 成功解析的命令列表
        - errors: 解析失败的错误信息列表
        """
        matches = CommandParser.PATTERN.findall(text)
        commands = []
        errors = []

        for match in matches:
            raw = match.strip()

            # 跳过不像 JSON 的代码块（不以 { 或 [ 开头）
            # 避免 AI 用代码块展示路径/文本时误报 JSON 解析错误
            if not raw or raw[0] not in '{[':
                continue

            cmd = None

            # 第一次尝试：直接解析
            try:
                cmd = json.loads(raw)
            except json.JSONDecodeError:
                # 第二次尝试：修复后重试
                try:
                    repaired = CommandParser._repair_json(raw)
                    cmd = json.loads(repaired)
                except json.JSONDecodeError as e2:
                    snippet = raw[:200] + "..." if len(raw) > 200 else raw
                    errors.append(
                        f"JSON parse failed: {e2}. Snippet: {snippet}"
                    )
                    continue

            if cmd is not None:
                if isinstance(cmd, dict) and "action" in cmd:
                    commands.append(cmd)
                elif isinstance(cmd, list):
                    for c in cmd:
                        if isinstance(c, dict) and "action" in c:
                            commands.append(c)

        return commands, errors


class LLMClient:
    def __init__(self, api_base: str, api_key: str):
        self.client = OpenAI(base_url=api_base, api_key=api_key)

    def chat(
        self, messages: list, model: str, temperature: float = 0.7, stream: bool = False
    ):
        return self.client.chat.completions.create(
            model=model, messages=messages, temperature=temperature, stream=stream
        )
