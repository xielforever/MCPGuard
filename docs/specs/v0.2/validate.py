#!/usr/bin/env python3
"""Offline v0.2 Schema/OpenAPI/SQL contract validation. Never applies migrations."""
import json
from pathlib import Path
from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import OpenAPIV31SpecValidator
from pglast import parse_sql

ROOT = Path(__file__).resolve().parent
schema = json.loads((ROOT / "contract.schema.json").read_text())
Draft202012Validator.check_schema(schema)
examples = json.loads((ROOT / "examples.json").read_text())
for i, example in enumerate(examples):
    test = {"$schema": schema["$schema"], "$defs": schema["$defs"], "$ref": "#/$defs/" + example["schema"]}
    valid = Draft202012Validator(test, format_checker=FormatChecker()).is_valid(example["value"])
    if valid != example["valid"]: raise SystemExit(f"v0.2 example {i} assertion failed")
api = json.loads((ROOT / "openapi.json").read_text())
OpenAPIV31SpecValidator(api).validate()
names = [v["operationId"] for path in api["paths"].values() for v in path.values()]
assert len(names) == len(set(names))
tools = json.loads((ROOT / "mcp-tools.json").read_text())
assert len({t["name"] for t in tools}) == len(tools)
for name, definition in schema["$defs"].items():
    if definition.get("type") == "object": assert definition.get("additionalProperties") is False
assert "password" not in schema["$defs"]["CredentialReference"]["properties"]
sql_files=[ROOT / "migration-draft.sql",*sorted((ROOT.parents[2]/"internal/resources/migrations").glob('*.sql')),*sorted((ROOT.parents[2]/"internal/resources/native_provider").glob('*.sql'))]
for sql in sql_files:
    parse_sql(sql.read_text())
print(json.dumps({"schemaVersion":"0.2","contractRevision":api['info']['version'],"definitions":len(schema["$defs"]),"examples":len(examples),"openapiOperations":len(names),"mcpToolContracts":len(tools),"sqlParsed":len(sql_files),"databaseWrites":0}))

