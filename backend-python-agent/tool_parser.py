from __future__ import annotations

import json
from typing import Optional


def parse_tool_call(llm_plan_json: str) -> Optional[tuple[str, dict]]:
    """Parse a tool call from the LLM plan JSON string.

    Expected format inside the JSON:
      {
        "tool_call": {
          "name": "tool_name",
          "arguments": {"arg1": "value"}
        }
      }

    Returns:
      (tool_name, args) if present, else None.
    """

    if not llm_plan_json:
        return None

    try:
        data = json.loads(llm_plan_json)
    except json.JSONDecodeError:
        return None

    if not isinstance(data, dict):
        return None

    tool_call = data.get("tool_call")
    if not isinstance(tool_call, dict):
        return None

    name = tool_call.get("name")
    args = tool_call.get("arguments")

    if not isinstance(name, str) or not name:
        return None

    if args is None:
        args = {}
    if not isinstance(args, dict):
        return None

    return name, args
