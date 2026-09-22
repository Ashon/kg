#!/usr/bin/env python3
# SPDX-FileCopyrightText: 2026 Ashon
# SPDX-License-Identifier: MIT

"""Strip server-populated fields so an exported object can be applied elsewhere."""

import sys

import yaml

SERVER_FIELDS = (
    "creationTimestamp",
    "resourceVersion",
    "uid",
    "generation",
    "managedFields",
    "selfLink",
)
SERVER_ANNOTATIONS = (
    "deprecated.daemonset.template.generation",
    "kubectl.kubernetes.io/last-applied-configuration",
)


def clean(doc):
    metadata = doc.setdefault("metadata", {})
    for field in SERVER_FIELDS:
        metadata.pop(field, None)

    annotations = metadata.get("annotations", {})
    for annotation in SERVER_ANNOTATIONS:
        annotations.pop(annotation, None)
    if not annotations:
        metadata.pop("annotations", None)

    doc.pop("status", None)

    template = doc.get("spec", {}).get("template")
    if isinstance(template, dict):
        template.get("metadata", {}).pop("creationTimestamp", None)

    return doc


def main():
    for path in sys.argv[1:]:
        with open(path, encoding="utf-8") as handle:
            doc = yaml.safe_load(handle)
        if not doc:
            continue
        print("---")
        print(yaml.dump(clean(doc), default_flow_style=False, sort_keys=False).rstrip())


if __name__ == "__main__":
    main()
