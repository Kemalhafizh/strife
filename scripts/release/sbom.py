#!/usr/bin/env python3
"""Writes an SPDX 2.3 SBOM for a Strife release.

Strife has no third-party dependencies, so the bill of materials is the
module itself and the Go toolchain (standard library) it was built with,
both read from the binary's embedded build information.

    scripts/release/sbom.py dist/strife VERSION > dist/strife.spdx.json
"""
import datetime
import json
import subprocess
import sys
import uuid

binary, version = sys.argv[1], sys.argv[2]
info = subprocess.run(["go", "version", "-m", binary], check=True, capture_output=True, text=True).stdout
lines = [line.split("\t") for line in info.splitlines()]
go_version = info.splitlines()[0].split(": ", 1)[-1].strip() if info else "unknown"
module = next((l[2] for l in lines if len(l) > 2 and l[1] == "mod"), "github.com/kemalhafizh/strife")
deps = [(l[2], l[3]) for l in lines if len(l) > 3 and l[1] == "dep"]
settings = {l[2].split("=", 1)[0]: l[2].split("=", 1)[1] for l in lines if len(l) > 2 and l[1] == "build" and "=" in l[2]}

def pkg(spdx_id, name, ver, purl):
    return {
        "SPDXID": spdx_id, "name": name, "versionInfo": ver, "downloadLocation": "NOASSERTION",
        "licenseConcluded": "NOASSERTION", "copyrightText": "NOASSERTION",
        "externalRefs": [{"referenceCategory": "PACKAGE-MANAGER", "referenceType": "purl", "referenceLocator": purl}],
    }

packages = [
    pkg("SPDXRef-strife", module, version, f"pkg:golang/{module}@{version}"),
    pkg("SPDXRef-go-stdlib", "stdlib", go_version, f"pkg:golang/stdlib@{go_version}"),
]
relationships = [
    {"spdxElementId": "SPDXRef-DOCUMENT", "relationshipType": "DESCRIBES", "relatedSpdxElement": "SPDXRef-strife"},
    {"spdxElementId": "SPDXRef-strife", "relationshipType": "DEPENDS_ON", "relatedSpdxElement": "SPDXRef-go-stdlib"},
]
for i, (name, ver) in enumerate(deps):
    packages.append(pkg(f"SPDXRef-dep-{i}", name, ver, f"pkg:golang/{name}@{ver}"))
    relationships.append({"spdxElementId": "SPDXRef-strife", "relationshipType": "DEPENDS_ON", "relatedSpdxElement": f"SPDXRef-dep-{i}"})
packages[0]["licenseDeclared"] = "Apache-2.0"
if "vcs.revision" in settings:
    packages[0]["sourceInfo"] = f"built from commit {settings['vcs.revision']}"

json.dump({
    "spdxVersion": "SPDX-2.3",
    "dataLicense": "CC0-1.0",
    "SPDXID": "SPDXRef-DOCUMENT",
    "name": f"strife-{version}",
    "documentNamespace": f"https://github.com/kemalhafizh/strife/releases/{version}/{uuid.uuid4()}",
    "creationInfo": {"created": datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
                     "creators": ["Tool: strife-release-sbom"]},
    "packages": packages,
    "relationships": relationships,
}, sys.stdout, indent=2)
print()
