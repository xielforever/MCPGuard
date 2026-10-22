#!/usr/bin/env python3
"""Validate design artifacts offline. Never applies SQL or connects to a backend."""
from __future__ import annotations

import copy
import json
from pathlib import Path
import sys
from urllib.parse import unquote, urldefrag, urlparse

import yaml
from jsonschema import Draft202012Validator, FormatChecker
from openapi_spec_validator import OpenAPIV31SpecValidator
from pglast import parse_sql, parser
from referencing import Registry, Resource

ROOT = Path(__file__).resolve().parent
FORMAT = FormatChecker()
STATS: dict[str, int] = {}


def require(condition: bool, message: str) -> None:
    if not condition:
        raise ValueError(message)


def unique_pairs(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f"duplicate JSON key: {key}")
        result[key] = value
    return result


class StrictLoader(yaml.SafeLoader):
    pass


def unique_mapping(loader, node, deep=False):
    result = {}
    for key_node, value_node in node.value:
        key = loader.construct_object(key_node, deep=deep)
        require(key not in result, f"duplicate YAML key: {key}")
        result[key] = loader.construct_object(value_node, deep=deep)
    return result


StrictLoader.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, unique_mapping
)


def load(path: Path):
    content = path.read_text(encoding="utf-8")
    if path.suffix == ".json":
        return json.loads(content, object_pairs_hook=unique_pairs)
    return yaml.load(content, Loader=StrictLoader)


DOCUMENTS = {
    path.resolve(): load(path)
    for pattern in ("*.json", "*.yaml")
    for path in ROOT.glob(pattern)
}
IDS = {
    doc["$id"]: path
    for path, doc in DOCUMENTS.items()
    if isinstance(doc, dict) and "$id" in doc
}


def resolve(ref: str, source: Path):
    address, fragment = urldefrag(ref)
    if not address:
        target = source
    elif address in IDS:
        target = IDS[address]
    else:
        parsed = urlparse(address)
        require(not parsed.scheme or parsed.scheme == "file",
                f"non-local reference is forbidden: {address}")
        target = (Path(unquote(parsed.path)) if parsed.scheme == "file"
                  else source.parent / unquote(address)).resolve()
    require(target.is_relative_to(ROOT), "reference escapes specs directory")
    require(target in DOCUMENTS, f"reference target not found: {target.name}")
    node = DOCUMENTS[target]
    if fragment:
        fragment = unquote(fragment)
        require(fragment.startswith("/"), f"unsupported anchor: {ref}")
        for part in fragment[1:].split("/"):
            part = part.replace("~1", "/").replace("~0", "~")
            node = node[int(part)] if isinstance(node, list) else node[part]
    return target, node, fragment


def walk_refs(node, source: Path):
    if isinstance(node, dict):
        if isinstance(node.get("$ref"), str):
            resolve(node["$ref"], source)
            STATS["referencesResolved"] = STATS.get("referencesResolved", 0) + 1
        for value in node.values():
            walk_refs(value, source)
    elif isinstance(node, list):
        for value in node:
            walk_refs(value, source)


def materialize(node, source: Path, stack=()):
    """Expand this acyclic, local design reference graph without network access."""
    if isinstance(node, list):
        return [materialize(v, source, stack) for v in node]
    if not isinstance(node, dict):
        return node
    if isinstance(node.get("$ref"), str):
        target, referenced, fragment = resolve(node["$ref"], source)
        identity = (target, fragment)
        require(identity not in stack, "cyclic graph needs a different bundler")
        expanded = materialize(referenced, target, (*stack, identity))
        siblings = {k: v for k, v in node.items() if k != "$ref"}
        if not siblings:
            return expanded
        require(isinstance(expanded, dict), "cannot merge boolean ref siblings")
        result = dict(expanded)
        for key, value in siblings.items():
            new_value = materialize(value, source, stack)
            if key in result and key not in ("description", "title", "summary"):
                require(result[key] == new_value, f"conflicting ref sibling: {key}")
            result[key] = new_value
        return result
    return {k: materialize(v, source, stack) for k, v in node.items()}


def deny_retrieval(uri):
    raise ValueError(f"network schema retrieval forbidden: {uri}")


REGISTRY = Registry(retrieve=deny_retrieval)
for path, doc in DOCUMENTS.items():
    if isinstance(doc, dict) and "$schema" in doc:
        resource = Resource.from_contents(doc)
        REGISTRY = REGISTRY.with_resource(path.as_uri(), resource)
        if "$id" in doc:
            REGISTRY = REGISTRY.with_resource(doc["$id"], resource)


def validator(schema):
    return Draft202012Validator(
        schema, registry=REGISTRY, format_checker=FORMAT
    )


def expect(schema, value, expected: bool, name: str):
    actual = validator(schema).is_valid(value)
    require(actual == expected, f"example assertion failed: {name}")
    label = "positiveExamples" if expected else "negativeExamples"
    STATS[label] = STATS.get(label, 0) + 1


def validate_schemas():
    for path in sorted(ROOT.glob("*.schema.json")):
        doc = DOCUMENTS[path.resolve()]
        Draft202012Validator.check_schema(doc)
        walk_refs(doc, path.resolve())
        STATS["schemasValidated"] = STATS.get("schemasValidated", 0) + 1


def validate_examples():
    cases = DOCUMENTS[(ROOT / "contract-examples.json").resolve()]["cases"]
    for case in cases:
        target, _, _ = resolve(case["schema"], (ROOT / "contract-examples.json").resolve())
        schema = {"$ref": target.as_uri() + ("#" + case["schema"].split("#", 1)[1] if "#" in case["schema"] else "")}
        expect(schema, case["value"], case["valid"], case["name"])
        value = case["value"]
        if case["valid"] and isinstance(value, dict) and value.get("kind") == "ACTION_CONTROL" and value["result"] is not None:
            action, result = value["action"], value["result"]
            for field in ("actionId", "executionId", "requestStatus", "dispatchState", "sideEffectStatus"):
                require(action[field] == result[field], "control/result facts disagree: " + case["name"])
            if result["requestStatus"] == "SUCCEEDED":
                tools = DOCUMENTS[(ROOT / "demo-tools.yaml").resolve()]["tools"]
                tool = next(t for t in tools if t["name"] == action["toolName"])
                require(validator(tool["resultDataSchema"]).is_valid(result["data"]),
                        "control result data does not match bound tool: " + case["name"])


def ensure_no_external_refs(node):
    if isinstance(node, dict):
        if isinstance(node.get("$ref"), str):
            require(node["$ref"].startswith("#/"),
                    "business schemas may not load external references")
        for value in node.values():
            ensure_no_external_refs(value)
    elif isinstance(node, list):
        for value in node:
            ensure_no_external_refs(value)


def validate_tools():
    bundle_path = (ROOT / "demo-tools.yaml").resolve()
    bundle = DOCUMENTS[bundle_path]
    validator(DOCUMENTS[(ROOT / "tool-bundle.schema.json").resolve()]).validate(bundle)
    names = set()
    common = materialize(
        DOCUMENTS[(ROOT / "tool-result.schema.json").resolve()],
        (ROOT / "tool-result.schema.json").resolve()
    )
    for annotation in ("$id", "$schema", "$defs"):
        common.pop(annotation, None)
    for tool in bundle["tools"]:
        require(tool["name"] not in names, "duplicate tool name")
        names.add(tool["name"])
        require(tool["outputSchema"] == {"$ref": "tool-result.schema.json"},
                "unexpected business output contract")
        for field in ("inputSchema", "resultDataSchema"):
            Draft202012Validator.check_schema(tool[field])
            ensure_no_external_refs(tool[field])
        for field in ("scopeId", "resourceId", "sql", "command", "url",
                      "headers", "credential", "approved"):
            require(field not in tool["inputSchema"].get("properties", {}),
                    f"untrusted business field: {tool['name']}/{field}")
        for field in tool["inputSchema"].get("required", []):
            require(field in tool["inputSchema"].get("properties", {}),
                    f"undefined required input: {tool['name']}/{field}")
        read = tool["effectClass"] == "READ"
        require(tool["interactionMode"] == ("BOUNDED_WAIT" if read else "CONTROL_FLOW"),
                "effect/interaction mismatch")
        require(tool["annotations"]["readOnlyHint"] == read,
                "effect/annotation mismatch")
        compiled = {
            "type": "object",
            "allOf": [
                copy.deepcopy(common),
                {
                    "if": {
                        "properties": {
                            "kind": {"const": "FINAL"},
                            "requestStatus": {"const": "SUCCEEDED"}
                        },
                        "required": ["kind", "requestStatus"]
                    },
                    "then": {"properties": {"data": tool["resultDataSchema"]}}
                }
            ]
        }
        Draft202012Validator.check_schema(compiled)
        groups = (
            ("validInputs", "inputSchema", True),
            ("invalidInputs", "inputSchema", False),
            ("successDataSamples", "resultDataSchema", True),
            ("invalidSuccessDataSamples", "resultDataSchema", False)
        )
        for group, schema_name, expected in groups:
            require(tool["examples"][group], f"missing examples: {tool['name']}/{group}")
            for index, value in enumerate(tool["examples"][group]):
                label = f"{tool['name']}/{group}/{index}"
                expect(tool[schema_name], value, expected, label)
                if schema_name == "resultDataSchema":
                    envelope = {
                        "kind": "FINAL",
                        "actionId": "11111111-1111-4111-8111-111111111111",
                        "attemptId": "22222222-2222-4222-8222-222222222222",
                        "executionId": "33333333-3333-4333-8333-333333333333",
                        "requestStatus": "SUCCEEDED", "dispatchState": "DISPATCHED",
                        "sideEffectStatus": "NONE" if read else "APPLIED",
                        "data": value, "error": None, "metadata": {}
                    }
                    expect(compiled, envelope, expected, label + "/compiled-output")
        STATS["toolsValidated"] = STATS.get("toolsValidated", 0) + 1


def validate_openapi():
    path = (ROOT / "openapi.yaml").resolve()
    document = DOCUMENTS[path]
    require(document.get("openapi") == "3.1.0", "unexpected OpenAPI dialect")
    walk_refs(document, path)
    expanded = materialize(document, path)
    # Full standard validator runs against a local-only dereferenced copy.
    # The checked-in source remains references plus reusable JSON Schemas.
    OpenAPIV31SpecValidator(expanded).validate()
    ids = set()
    for path_name, path_item in document["paths"].items():
        for method, operation in path_item.items():
            if method not in ("get", "post", "patch", "put", "delete", "options", "head"):
                continue
            require(operation["operationId"] not in ids, "duplicate operationId")
            ids.add(operation["operationId"])
    STATS["openapiOperationsValidated"] = len(ids)


def validate_sql():
    sql = (ROOT / "metadata-model.sql").read_text(encoding="utf-8")
    STATS["sqlStatementsParsed"] = len(parse_sql(sql))
    STATS["plpgsqlFunctionsParsed"] = len(json.loads(parser.parse_plpgsql_json(sql)))


def main():
    validate_schemas()
    validate_examples()
    validate_tools()
    validate_openapi()
    validate_sql()
    print(json.dumps({"status": "PASS", **STATS,
                      "databaseExecuted": False, "serviceIntegrationRun": False},
                     ensure_ascii=False, indent=2))


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(f"FAIL {type(error).__name__}: {str(error).splitlines()[0][:300]}",
              file=sys.stderr)
        sys.exit(1)
