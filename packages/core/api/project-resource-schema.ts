import { z } from "zod";
import type { ProjectResource } from "../types";

const LocalDirectoryRefSchema = z.object({
  local_path: z.string(),
  daemon_id: z.string(),
  label: z.string().optional(),
  execution_mode: z.string().optional(),
  init_script: z.string().optional(),
});

export const ProjectResourceSchema = z.object({
  id: z.string(),
  project_id: z.string(),
  workspace_id: z.string(),
  resource_type: z.string(),
  resource_ref: z.record(z.string(), z.unknown()),
  label: z.string().nullable().default(null),
  position: z.number().default(0),
  created_at: z.string().default(""),
  created_by: z.string().nullable().default(null),
}).superRefine((resource, ctx) => {
  if (resource.resource_type === "local_directory") {
    const result = LocalDirectoryRefSchema.safeParse(resource.resource_ref);
    if (!result.success) {
      for (const issue of result.error.issues) {
        ctx.addIssue({ code: "custom", path: ["resource_ref", ...issue.path], message: issue.message });
      }
    }
  }
});

export const ListProjectResourcesResponseSchema = z.object({
  resources: z.array(ProjectResourceSchema).default([]),
  total: z.number().default(0),
});

export const EMPTY_PROJECT_RESOURCE: ProjectResource = {
  id: "", project_id: "", workspace_id: "", resource_type: "local_directory",
  resource_ref: { local_path: "", daemon_id: "" }, label: null,
  position: 0, created_at: "", created_by: null,
};
