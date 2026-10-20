"use client";

import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { mattermostKeys } from "@multica/core/mattermost";
import type { MattermostDeniedReply, MattermostInstallation } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { useT } from "../../i18n";

export type MattermostReplyDraft = NonNullable<MattermostInstallation["denied_reply"]> & { file?: File };

export async function prepareMattermostDeniedReply(draft: MattermostReplyDraft): Promise<MattermostDeniedReply> {
  if (draft.mode === "text") return { mode: "text", text: draft.text ?? "" };
  if (draft.mode !== "image") throw new Error("Choose an automatic reply type");
  const imageId = draft.file ? (await api.uploadFile(draft.file)).id : draft.image_attachment_id;
  if (!imageId) throw new Error("Automatic reply image upload failed");
  return { mode: "image", image_attachment_id: imageId };
}

export function deniedReplyMatches(saved: MattermostInstallation["denied_reply"], expected: MattermostDeniedReply): boolean {
  return (saved?.mode ?? "text") === expected.mode && (saved?.text ?? "") === (expected.text ?? "") &&
    (saved?.image_attachment_id ?? "") === (expected.image_attachment_id ?? "");
}

export function MattermostDeniedReplyFields({ value, onChange, disabled }: {
  value: MattermostReplyDraft;
  onChange: (value: MattermostReplyDraft) => void;
  disabled: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const wsId = useWorkspaceId();
  const [localPreview, setLocalPreview] = useState<string>();
  useEffect(() => {
    if (!value.file) { setLocalPreview(undefined); return; }
    const url = URL.createObjectURL(value.file);
    setLocalPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [value.file]);
  const imageId = value.mode === "image" ? value.image_attachment_id : undefined;
  const { data: image, isError } = useQuery({
    queryKey: mattermostKeys.replyImage(wsId, imageId ?? ""),
    queryFn: () => api.getAttachment(imageId ?? ""),
    enabled: !!imageId && !value.file,
    staleTime: 0,
  });
  const preview = localPreview ?? resolvePublicFileUrl(image?.download_url);
  const options = [
    { value: "text", label: t(($) => $.mattermost.denied_reply.text) },
    { value: "image", label: t(($) => $.mattermost.denied_reply.image) },
  ];
  return (
    <div className="space-y-2">
      <Label htmlFor={`${id}-mode`}>{t(($) => $.mattermost.denied_reply.label)}</Label>
      <Select items={options} value={value.mode === "unknown" ? null : value.mode} disabled={disabled}
        onValueChange={(mode) => { if (mode === "text" || mode === "image") onChange({ mode }); }}>
        <SelectTrigger id={`${id}-mode`} className="w-full" aria-describedby={`${id}-help`}>
          <SelectValue placeholder={t(($) => $.mattermost.denied_reply.unknown)} />
        </SelectTrigger>
        <SelectContent>{options.map((option) => (
          <SelectItem key={option.value} value={option.value}>{option.label}</SelectItem>
        ))}</SelectContent>
      </Select>
      <p id={`${id}-help`} className="text-caption text-muted-foreground">{t(($) => $.mattermost.denied_reply.help)}</p>
      {value.mode === "text" && <>
        <Label htmlFor={`${id}-text`}>{t(($) => $.mattermost.denied_reply.text_label)}</Label>
        <Textarea id={`${id}-text`} value={value.text ?? ""} maxLength={4000} disabled={disabled} rows={3}
          aria-describedby={`${id}-text-help`} onChange={(event) => onChange({ mode: "text", text: event.target.value })} />
        <p id={`${id}-text-help`} className="text-caption text-muted-foreground">{t(($) => $.mattermost.denied_reply.text_help)}</p>
      </>}
      {value.mode === "image" && <>
        <Label htmlFor={`${id}-file`}>{t(($) => $.mattermost.denied_reply.image_label)}</Label>
        <Input id={`${id}-file`} type="file" accept="image/png,image/jpeg,image/gif,image/webp" disabled={disabled}
          aria-describedby={`${id}-image-help`} onChange={(event) => {
            const file = event.target.files?.[0];
            event.target.value = "";
            if (!file) return;
            if (!["image/png", "image/jpeg", "image/gif", "image/webp"].includes(file.type) || file.size > 5 * 1024 * 1024) {
              toast.error(t(($) => $.mattermost.denied_reply.image_help));
              return;
            }
            onChange({ mode: "image", file });
          }} />
        <p id={`${id}-image-help`} className="text-caption text-muted-foreground">{t(($) => $.mattermost.denied_reply.image_help)}</p>
        {(value.file?.name || image?.filename) && <p className="break-words text-caption">{value.file?.name ?? image?.filename}</p>}
        {preview && <img src={preview} alt={t(($) => $.mattermost.denied_reply.preview)} className="max-h-48 max-w-full rounded-md object-contain" />}
        {isError && !value.file && <p role="alert" className="text-caption text-destructive">{t(($) => $.mattermost.denied_reply.preview_failed)}</p>}
      </>}
    </div>
  );
}
