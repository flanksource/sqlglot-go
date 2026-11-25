import { post } from "../api";

export const PROJECT_CONFIG = ".sqlglot-go.yaml";
export type BundleFile = { path: string; content: string };
/** `deselected` counts the mapped sources the project's default label `select` left out; the server omits it when none. */
export type ProjectPreview = { mapped: number; ignored: number; deselected?: number };

export function previewText({ mapped, ignored, deselected = 0 }: ProjectPreview): string {
  return `${mapped} mapped · ${ignored} ignored${deselected > 0 ? ` · ${deselected} deselected` : ""}`;
}
export type LocalProject = {
  config?: string;
  targets?: string[];
  preview?: ProjectPreview;
  error?: string;
};

export const PROJECT_TARGET_PREFIX = "project://";

export async function uploadMigrationBundle(files: FileList): Promise<{
  files: BundleFile[];
  preview?: ProjectPreview;
}> {
  const byPath = new Map(Array.from(files, (file) => [
    file.webkitRelativePath.split("/").slice(1).join("/") || file.name,
    file,
  ]));
  const config = byPath.get(PROJECT_CONFIG);
  let paths = [...byPath.keys()];
  let mapped = false;
  if (config) {
    const selection = await post("/migrate/sources", { config: await config.text(), paths }) as { required: string[]; plain?: boolean };
    paths = selection.required;
    mapped = !selection.plain;
  }
  const selected = await Promise.all(paths.map(async (path) => {
    const file = byPath.get(path);
    if (!file) throw new Error(`Upload is missing ${path}`);
    return { path, content: new TextDecoder("utf-8", { ignoreBOM: true }).decode(await file.arrayBuffer()) };
  }));
  if (!mapped) return { files: selected };
  const preview = await post("/migrate/sources", { files: selected }) as ProjectPreview;
  return { files: selected, preview };
}
