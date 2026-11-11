#!/usr/bin/env python3
"""Single-run, reviewed, fail-closed resolutions for pinned upstream 079ec9f6."""
import json
import pathlib
import re
import subprocess

def git(*args):
    return subprocess.check_output(["git", *args], text=True)

def stage(path, n):
    return git("show", f":{n}:{path}")

def write(path, data):
    pathlib.Path(path).write_text(data)

def merge_json(base, ours, theirs, where="root"):
    missing = object()
    def walk(b, o, t, path):
        if o == t:
            return o
        if o == b:
            return t
        if t == b:
            return o
        if isinstance(o, dict) and isinstance(t, dict) and (isinstance(b, dict) or b is missing):
            obj = {}
            keys = list(dict.fromkeys([*t.keys(), *o.keys(), *(b.keys() if isinstance(b, dict) else [])]))
            for key in keys:
                value = walk(b.get(key, missing) if isinstance(b, dict) else missing,
                             o.get(key, missing), t.get(key, missing), path + "." + key)
                if value is not missing:
                    obj[key] = value
            return obj
        raise RuntimeError(f"Conflicting edits require manual review: {path}")
    # A shared missing sentinel must be used for all levels; reroute below.
    return walk(base, ours, theirs, where)

def merge_json3(base, ours, theirs, path="root"):
    SENTINEL = object()
    def visit(b, o, t, name):
        if o == t: return o
        if o == b: return t
        if t == b: return o
        if isinstance(o,dict) and isinstance(t,dict) and (b is SENTINEL or isinstance(b,dict)):
            keys=list(dict.fromkeys([*t.keys(),*o.keys(),*(b.keys() if isinstance(b,dict) else [])]))
            result={}
            for key in keys:
                value=visit(b.get(key,SENTINEL) if isinstance(b,dict) else SENTINEL,
                            o.get(key,SENTINEL),t.get(key,SENTINEL),name+"."+key)
                if value is not SENTINEL: result[key]=value
            return result
        raise RuntimeError("Manual semantic review required for: "+name)
    return visit(base,ours,theirs,path)

def replace_conflict(path, resolver):
    source = pathlib.Path(path).read_text()
    pattern = re.compile(r"(?ms)^<<<<<<< HEAD\n(.*?)^=======\n(.*?)^>>>>>>> [^\n]*\n")
    count = [0]
    def sub(m):
        count[0] += 1
        return resolver(m.group(1),m.group(2))
    output = pattern.sub(sub,source)
    if count[0]!=1 or "<<<<<<<" in output:
        raise RuntimeError(f"{path}: expected one well-formed conflict, found {count[0]}")
    write(path,output)

expected={".github/workflows/ci.yml","scripts/ci-scope.test.mjs",
          "server/cmd/server/router.go","server/internal/handler/agent.go"}
expected.update(f"apps/web/features/landing/i18n/{lang}.ts" for lang in ["en","ja","ko","zh"])
expected.update(f"packages/views/locales/{lang}/agents.json" for lang in
                ["en","es","fr","ja","ko","ru","zh-Hans","zh-Hant"])
conflicts=set(git("diff","--name-only","--diff-filter=U").splitlines())
if conflicts!=expected:
    raise RuntimeError(f"Unexpected conflicts; refusing automatic resolution: missing={expected-conflicts}, extra={conflicts-expected}")

# CI is reviewed separately and copied from the fork: credentials/required
# gates must not disappear, and ephemeral GITHUB_TOKEN cannot change workflows.
write(".github/workflows/ci.yml", stage(".github/workflows/ci.yml",2))

# Upstream completely replaced the old marketing landing page with site-xx
# dictionaries. The fork delta was ONE obsolete marketing sentence per locale;
# preserve the actual plugin implementation, not the removed page wording.
for lang in ["en","ja","ko","zh"]:
    path=f"apps/web/features/landing/i18n/{lang}.ts"
    write(path,stage(path,3))

# Merge locales by JSON key, not position or lexical conflict preference.
# Fail if the same translated key was edited differently in both branches.
for lang in ["en","es","fr","ja","ko","ru","zh-Hans","zh-Hant"]:
    path=f"packages/views/locales/{lang}/agents.json"
    parsed=[json.loads(stage(path,n)) for n in [1,2,3]]
    result=merge_json3(*parsed,path)
    write(path,json.dumps(result,ensure_ascii=False,indent=2)+"\n")

# Fork marketplace-sync route and upstream environment-control routes coexist.
replace_conflict("server/cmd/server/router.go",
                 lambda ours,theirs: ours.rstrip("\n")+"\n"+theirs.lstrip("\n"))

# Preserve the fork's per-task Codex plugin context AND upstream private-project
# restricted-task flag. Shared cancellation fields come from upstream.
def agent_fields(ours,theirs):
    anchor="\tStartClaimSupported"
    if anchor not in theirs: raise RuntimeError("upstream task fields changed")
    plugin=[line for line in ours.splitlines(keepends=True) if "AgentPlugins " in line]
    if len(plugin)!=1: raise RuntimeError("fork agent-plugin field changed")
    pos=theirs.index(anchor)
    return theirs[:pos] + plugin[0] + theirs[pos:]
replace_conflict("server/internal/handler/agent.go",agent_fields)

# Keep the fork's platform-matrix invariant AND upstream's fail-closed quality
# gate test. The common closing '});' after the conflict closes upstream test.
replace_conflict("scripts/ci-scope.test.mjs",
                 lambda ours,theirs: ours.rstrip("\n")+"\n});\n\n"+theirs)
p=pathlib.Path("scripts/ci-scope.test.mjs")
text=p.read_text()
old="assert.match(action, /continue-on-error: true/);"
if text.count(old) > 1: raise RuntimeError("Ambiguous quality assertion")
if text.count(old) == 1:
    text=text.replace(old,"assert.doesNotMatch(action, /continue-on-error:/);")
if "assert.doesNotMatch(action, /continue-on-error:/);" not in text:
    raise RuntimeError("Missing fail-closed quality assertion")
p.write_text(text)

subprocess.run(["gofmt","-w","server/cmd/server/router.go","server/internal/handler/agent.go"],check=True)
subprocess.run(["git","add","--",*sorted(expected)],check=True)
remaining=git("diff","--name-only","--diff-filter=U")
if remaining.strip(): raise RuntimeError("Unmerged paths remain: "+remaining)
print("Resolved exact conflicts with per-key translation union and preserved fork integrations:")
for path in sorted(expected): print(path)
