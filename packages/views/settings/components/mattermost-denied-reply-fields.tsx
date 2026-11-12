"use client";

import { useEffect, useId, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { useWorkspaceId } from "@multica/core/hooks";
import { mattermostKeys } from "@multica/core/mattermost";
import type { MattermostDeniedReply, MattermostInstallation } from "@multica/core/types";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { Input } from "@multica/ui/components/ui/input";
import { Button } from "@multica/ui/components/ui/button";
import { Label } from "@multica/ui/components/ui/label";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@multica/ui/components/ui/select";
import { toast } from "sonner";
import { X } from "@multica/ui/icons";
import { useT } from "../../i18n";

export type MattermostReplyDraft = NonNullable<MattermostInstallation["denied_reply"]> & { files?: File[] };

export async function prepareMattermostDeniedReply(
  draft: MattermostReplyDraft,
  onProgress: (draft: MattermostReplyDraft) => void,
  workspaceId: string,
): Promise<MattermostDeniedReply> {
  if (draft.mode === "text") return { mode: "text", text: draft.text ?? "" };
  if (draft.mode !== "images") throw new Error("Choose an automatic reply type");
  const imageIds = [...(draft.image_attachment_ids ?? [])];
  const files = draft.files ?? [];
  for (const [index, file] of files.entries()) {
    const image = await api.uploadFile(file, { workspaceId });
    if (!image.id) throw new Error("Automatic reply image upload failed");
    imageIds.push(image.id);
    onProgress({ mode: "images", image_attachment_ids: [...imageIds], files: files.slice(index + 1) });
  }
  if (imageIds.length === 0) throw new Error("Choose an automatic reply image");
  return { mode: "images", image_attachment_ids: imageIds };
}

export function deniedReplyMatches(saved: MattermostInstallation["denied_reply"], expected: MattermostDeniedReply): boolean {
  return (saved?.mode ?? "text") === expected.mode && (saved?.text ?? "") === (expected.text ?? "") &&
    (saved?.image_attachment_ids ?? []).join("\n") === (expected.image_attachment_ids ?? []).join("\n");
}

export function MattermostDeniedReplyFields({ value, onChange, disabled }: {
  value: MattermostReplyDraft;
  onChange: (value: MattermostReplyDraft) => void;
  disabled: boolean;
}) {
  const { t } = useT("settings");
  const id = useId();
  const options = [
    { value: "text", label: t(($) => $.mattermost.denied_reply.text) },
    { value: "images", label: t(($) => $.mattermost.denied_reply.image) },
  ];
  return (
    <div className="space-y-2">
      <Label htmlFor={`${id}-mode`}>{t(($) => $.mattermost.denied_reply.label)}</Label>
      <Select items={options} value={value.mode === "unknown" ? null : value.mode} disabled={disabled}
        onValueChange={(mode) => { if (mode === "text" || mode === "images") onChange({ mode }); }}>
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
      {value.mode === "images" && <>
        <Label htmlFor={`${id}-file`}>{t(($) => $.mattermost.denied_reply.image_label)}</Label>
        <Input id={`${id}-file`} type="file" multiple accept="image/png,image/jpeg,image/gif,image/webp" disabled={disabled}
          aria-describedby={`${id}-image-help`} onChange={(event) => {
            const files = Array.from(event.target.files ?? []);
            event.target.value = "";
            if (files.length === 0) return;
            if (files.some((file) => !["image/png", "image/jpeg", "image/gif", "image/webp"].includes(file.type) || file.size > 5 * 1024 * 1024)) {
              toast.error(t(($) => $.mattermost.denied_reply.image_help));
              return;
            }
            onChange({ ...value, files: [...(value.files ?? []), ...files] });
          }} />
        <p id={`${id}-image-help`} className="text-caption text-muted-foreground">{t(($) => $.mattermost.denied_reply.image_help)}</p>
        <div className="space-y-2">
          {(value.image_attachment_ids ?? []).map((imageId, index) => (
            <MattermostReplyImage key={imageId} imageId={imageId} disabled={disabled}
              onRemove={() => onChange({ ...value, image_attachment_ids: value.image_attachment_ids?.filter((_, i) => i !== index) })} />
          ))}
          {(value.files ?? []).map((file, index) => (
            <MattermostReplyImage key={`file-${index}`} file={file} disabled={disabled}
              onRemove={() => onChange({ ...value, files: value.files?.filter((_, i) => i !== index) })} />
          ))}
        </div>
      </>}
    </div>
  );
}

function MattermostReplyImage({ imageId, file, disabled, onRemove }: {
  imageId?: string;
  file?: File;
  disabled: boolean;
  onRemove: () => void;
}) {
  const { t } = useT("settings");
  const wsId = useWorkspaceId();
  const [localPreview, setLocalPreview] = useState<string>();
  useEffect(() => {
    if (!file) { setLocalPreview(undefined); return; }
    const url = URL.createObjectURL(file);
    setLocalPreview(url);
    return () => URL.revokeObjectURL(url);
  }, [file]);
  const { data: image, isError } = useQuery({
    queryKey: mattermostKeys.replyImage(wsId, imageId ?? ""),
    queryFn: () => api.getAttachment(imageId ?? ""),
    enabled: !!imageId,
    staleTime: 0,
  });
  const preview = localPreview ?? resolvePublicFileUrl(image?.download_url);
  const name = file?.name ?? image?.filename ?? t(($) => $.mattermost.denied_reply.preview);
  return (
    <div className="flex items-center gap-3">
      {preview && <img src={preview} alt={t(($) => $.mattermost.denied_reply.preview)} className="h-20 w-20 shrink-0 rounded-md object-contain" />}
      <div className="min-w-0 flex-1">
        <p className="break-words text-caption">{name}</p>
        {isError && <p role="alert" className="text-caption text-destructive">{t(($) => $.mattermost.denied_reply.preview_failed)}</p>}
      </div>
      <Button type="button" variant="ghost" size="icon-sm" disabled={disabled} onClick={onRemove}
        aria-label={t(($) => $.mattermost.denied_reply.remove_image, { name })}>
        <X className="h-4 w-4" />
      </Button>
    </div>
  );
}
