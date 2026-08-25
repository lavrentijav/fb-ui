import { z } from 'zod';

export const FilterListSchema = z.object({
  id: z.number(),
  name: z.string(),
  remark: z.string().optional(),
  kind: z.enum(['domain', 'ip']).optional(),
  // Serialized as null for a list saved without entries.
  entries: z.array(z.string()).nullish(),
  enable: z.boolean().optional(),
  createdAt: z.number().optional(),
  updatedAt: z.number().optional(),
});

export const FilterRuleSchema = z.object({
  id: z.number(),
  name: z.string(),
  remark: z.string().optional(),
  panelId: z.number().optional(),
  sourceInboundTags: z.array(z.string()).nullish(),
  listIds: z.array(z.number()).nullish(),
  action: z.enum(['block', 'direct', 'cascade']).optional(),
  cascadeLinkId: z.number().optional(),
  sortOrder: z.number().optional(),
  enable: z.boolean().optional(),
  applied: z.number().optional(),
});

export const FilterListsSchema = z.array(FilterListSchema);
export const FilterRulesSchema = z.array(FilterRuleSchema);

// The form keeps entries as one textarea; the API takes an array.
export const FilterListFormSchema = z.object({
  id: z.number().optional(),
  name: z.string().trim().min(1, 'pages.filters.toasts.fillRequired'),
  remark: z.string().optional(),
  kind: z.enum(['domain', 'ip']),
  entriesText: z.string(),
  enable: z.boolean(),
});

// A rule's required fields depend on its action, so the check is a refinement.
export const FilterRuleFormSchema = z
  .object({
    id: z.number().optional(),
    name: z.string().trim().min(1, 'pages.filters.toasts.fillRequired'),
    remark: z.string().optional(),
    panelId: z.number(),
    sourceInboundTags: z.array(z.string()),
    listIds: z.array(z.number()).min(1, 'pages.filters.toasts.needList'),
    action: z.enum(['block', 'direct', 'cascade']),
    cascadeLinkId: z.number().optional(),
    sortOrder: z.number().optional(),
    enable: z.boolean(),
  })
  .superRefine((val, ctx) => {
    if (val.action === 'cascade' && !val.cascadeLinkId) {
      ctx.addIssue({
        code: 'custom',
        path: ['cascadeLinkId'],
        message: 'pages.filters.toasts.needLink',
      });
    }
  });

export type FilterRuleFormValues = z.infer<typeof FilterRuleFormSchema>;
export type FilterList = z.infer<typeof FilterListSchema>;
export type FilterRule = z.infer<typeof FilterRuleSchema>;
export type FilterListFormValues = z.infer<typeof FilterListFormSchema>;
