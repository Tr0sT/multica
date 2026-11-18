#!/usr/bin/env python3
"""Temporary reviewed 2026-11-18 upstream/fork three-way conflict reconciliation.

Only used on ephemeral sync branch. It aborts if the conflict shape changes.
Keeps upstream HEAD (stage 2) and fork main (stage 3) semantically combined.
"""
import json
import pathlib
import re
import subprocess

EXPECTED = {
    "AGENTS.md",
    *("apps/docs/content/docs/channels."+s+".mdx" for s in ("ja","ko","zh")),
    "apps/docs/content/docs/channels.mdx",
    "packages/views/chat/components/chat-session-header.tsx",
    *("packages/views/locales/"+s+"/chat.json" for s in ("en","es","fr","ja","ko","ru","zh-Hans","zh-Hant")),
    "packages/views/settings/components/channel-connect-dialog.tsx",
    "server/internal/daemon/daemon.go",
    "server/internal/daemon/prompt.go",
    "server/internal/handler/webhook_delivery_test.go",
    "server/internal/service/autopilot.go",
    "server/pkg/agent/agent.go",
}
def run(*args):
    return subprocess.check_output(args, text=True).rstrip("\n")
def stage(path, num):
    return run("git","show",f":{num}:{path}")+"\n"
def write(path, content):
    pathlib.Path(path).write_text(content, encoding="utf-8")
    subprocess.run(["git","add","--",path],check=True)
P = re.compile(r"(?m)^<<<<<<< HEAD\n(.*?)^=======\n(.*?)^>>>>>>> origin/main\n",re.S)
def resolve(path, handler):
    content=pathlib.Path(path).read_text(encoding="utf-8")
    count=0
    def sub(m):
        nonlocal count
        count+=1
        return handler(m.group(1),m.group(2),count)
    result=P.sub(sub,content)
    if not count or "<<<<<<<" in result or ">>>>>>>" in result:
        raise RuntimeError(f"Unresolved or missing conflict in {path} ({count} hunks)")
    write(path,result)
    print(f"Resolved {path}: {count} hunks")
def docs(path):
    def reconcile(a,b,n):
        aa=a.strip("\n").split("\n")
        bb=b.strip("\n").split("\n")
        if aa[0].startswith("|") and bb[0].startswith("|"):
            if len(aa)!=len(bb): raise RuntimeError(f"{path}: table row counts differ")
            result=[]
            for l,r in zip(aa,bb):
                la=[x.strip() for x in l.strip().strip("|").split("|")]
                rb=[x.strip() for x in r.strip().strip("|").split("|")]
                if len(la)!=len(rb) or la[:-1]!=rb[:-1]:
                    raise RuntimeError(f"{path}: common table columns differ at hunk {n}")
                result.append("| " + " | ".join(la+[rb[-1]]) + " |")
            return "\n".join(result)+"\n"
        if len(aa)==1 and len(bb)==1 and aa[0].startswith("description:"):
            if "Mattermost" in aa[0]: return a
            return aa[0].rstrip()+" Mattermost\n"
        # Separate platform links and separate encryption variables are additive.
        if all((x.lstrip().startswith("- ") or x.lstrip().startswith("MULTICA_") or not x.strip()) for x in aa+bb):
            return a.rstrip("\n")+"\n"+b
        # English WeChat explanatory paragraph + fork-specific Mattermost link.
        if "Mattermost" in b and "WeChat" in a:
            return a.rstrip("\n")+"\n"+b
        raise RuntimeError(f"Need manual doc merge {path} hunk {n}:\n{a}\n---\n{b}")
    resolve(path,reconcile)

actual=set(run("git","diff","--name-only","--diff-filter=U").splitlines())
if actual!=EXPECTED: raise RuntimeError(f"Unexpected conflict set. Missing {EXPECTED-actual}, added {actual-EXPECTED}")
resolve("AGENTS.md",lambda a,b,n:a+b)
for p in sorted(x for x in EXPECTED if x.startswith("apps/docs/")):
    docs(p)
for p in sorted(x for x in EXPECTED if x.startswith("packages/views/locales/")):
    def json_conf(a,b,n):
        if not (a.startswith('  "wakeup": {') and b.startswith('  "owner_filter": {')):
            raise RuntimeError(f"Unexpected JSON collision in {p}")
        return a.rstrip()+"\n  },\n"+b
    resolve(p,json_conf)
    json.loads(pathlib.Path(p).read_text(encoding="utf-8"))
def header(a,b,n):
    if n==1:
        assert "ChatWakeupHeaderButton" in a and "chatChannelLabel" in b
        return a+b
    if n==2:
        assert "leaf={leaf}" in a and "readOnly ? null" in b
        return b.rstrip("\n").replace(
            "actions={readOnly ? null : (",
            'actions={readOnly ? null : (\n          <div className="flex items-center gap-0.5">\n            <ChatWakeupHeaderButton sessionId={session.id} wakeups={wakeups} />'
        )+"\n"
    if n==3:
        assert "</div>" in a and ")}" in b
        return "          </div>\n        )}\n"
    raise RuntimeError("Unknown header hunk")
resolve("packages/views/chat/components/chat-session-header.tsx",header)
def dialog(a,b,n):
    if n==1:
        assert "WeixinChannelAuthorization" in a and "MattermostAgentBindButton" in b
        return a+b
    if n==2:
        assert 'channel === "weixin"' in a and 'channel !== "mattermost"' in b
        return a.replace('channel === "weixin" ? null : (','channel === "weixin" || channel === "mattermost" ? null : (')
    raise RuntimeError("Unknown channel dialog hunk")
resolve("packages/views/settings/components/channel-connect-dialog.tsx",dialog)
resolve("server/internal/daemon/daemon.go",lambda a,b,n:a+b)
resolve("server/pkg/agent/agent.go",lambda a,b,n:a+b)
p="server/internal/daemon/prompt.go"
fork=stage(p,3)
ix=fork.index("\tif task.ChatChannelType == execenv.ChannelTypeMattermost {")
jx=fork.index("\n\tif task.Agent != nil",ix)
mattermost=fork[ix:jx].rstrip()+"\n"
def prompt(a,b,n):
    assert n==1 and "refs := opts.chatSkillRefs" in a and "mattermost-readonly" in b
    return mattermost + a
resolve(p,prompt)
p="server/internal/handler/webhook_delivery_test.go"
up=stage(p,2)
fork=stage(p,3)
m=re.search(r"(?ms)^func TestWebhookHandler_DistinctEventsCreateSameTitleIssues\(.*?(?=^func |\Z)",fork)
assert m and "TestWebhookDeliveryWorker_DistinctDeliveriesCreateSameTitleIssues" in up
assert "TestWebhookHandler_DistinctEventsCreateSameTitleIssues" not in up
assert "func TestWebhookHandler_DedupeViaGitHubDelivery" in up
up=up.replace('"github.com/multica-ai/multica/server/internal/events"', '"github.com/multica-ai/multica/server/internal/events"\n\t"github.com/multica-ai/multica/server/internal/testutil"')
up=up.replace("func TestWebhookHandler_DedupeViaGitHubDelivery",m.group(0)+"\nfunc TestWebhookHandler_DedupeViaGitHubDelivery",1)
write(p,up)
print("Resolved webhook test: kept both distinct regression tests")
def autopilot(a,b,n):
    assert n==1 and "if run.WebhookDeliveryID.Valid" in a and "run.WebhookDeliveryID" in b
    assert "projectID, title, autopilotRecentDuplicateWindow" in a
    return a.replace("projectID, title, autopilotRecentDuplicateWindow","projectID, run.WebhookDeliveryID, title, autopilotRecentDuplicateWindow")
resolve("server/internal/service/autopilot.go",autopilot)
remain=run("git","diff","--name-only","--diff-filter=U")
if remain: raise RuntimeError(f"Unresolved files: {remain}")
for p in ("server/internal/daemon/daemon.go","server/internal/daemon/prompt.go","server/internal/service/autopilot.go","server/internal/handler/webhook_delivery_test.go","server/pkg/agent/agent.go"):
    subprocess.run(["gofmt","-w",p],check=True)
    subprocess.run(["git","add",p],check=True)
for key in ("mattermost","weixin"):
    text=pathlib.Path("packages/views/settings/components/channel-connect-dialog.tsx").read_text().lower()
    if key not in text: raise RuntimeError("Lost channel "+key)
if "CodexPluginArgs" not in pathlib.Path("server/internal/daemon/daemon.go").read_text(): raise RuntimeError("Lost upstream plugins")
if "SkillInvocation" not in pathlib.Path("server/pkg/agent/agent.go").read_text(): raise RuntimeError("Lost upstream skills")
if "ChatWakeupHeaderButton" not in pathlib.Path("packages/views/chat/components/chat-session-header.tsx").read_text(): raise RuntimeError("Lost wakeups")
subprocess.run(["git","diff","--check","--cached"],check=True)
print("All known conflict resolutions staged and syntax-checked")
