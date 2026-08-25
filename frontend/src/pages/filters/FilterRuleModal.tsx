import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Form, Input, Modal, Select, Switch, message } from 'antd';
import { FormProvider, useForm, useWatch } from 'react-hook-form';

import type { Msg } from '@/utils';
import {
  FilterRuleFormSchema,
  type FilterList,
  type FilterRule,
  type FilterRuleFormValues,
} from '@/schemas/filter';
import type { CascadeLink, GraphPanel } from '@/schemas/network';
import { FormField, rhfZodValidate } from '@/components/form/rhf';

interface FilterRuleModalProps {
  open: boolean;
  mode: 'add' | 'edit';
  rule: FilterRule | null;
  panels: GraphPanel[];
  links: CascadeLink[];
  lists: FilterList[];
  save: (payload: Partial<FilterRule>) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaultValues(rule: FilterRule | null, panels: GraphPanel[]): FilterRuleFormValues {
  return {
    id: rule?.id ?? 0,
    name: rule?.name ?? '',
    remark: rule?.remark ?? '',
    panelId: rule?.panelId ?? panels[0]?.id ?? 0,
    sourceInboundTags: rule?.sourceInboundTags ?? [],
    listIds: rule?.listIds ?? [],
    action: (rule?.action as FilterRuleFormValues['action']) ?? 'block',
    cascadeLinkId: rule?.cascadeLinkId ?? undefined,
    sortOrder: rule?.sortOrder ?? 0,
    enable: rule?.enable ?? true,
  };
}

// One filtering node: which inbounds it watches, which lists it matches, and
// where the matched traffic goes.
export default function FilterRuleModal({
  open,
  mode,
  rule,
  panels,
  links,
  lists,
  save,
  onOpenChange,
}: FilterRuleModalProps) {
  const { t } = useTranslation();
  const methods = useForm<FilterRuleFormValues>({ defaultValues: defaultValues(rule, panels) });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<{ mode: string; rule: FilterRule | null } | null>(null);
  if (!open) {
    if (synced) setSynced(null);
  } else if (!synced || synced.mode !== mode || synced.rule !== (rule ?? null)) {
    setSynced({ mode, rule: rule ?? null });
    methods.reset(defaultValues(mode === 'edit' ? (rule ?? null) : null, panels));
  }

  const panelId = useWatch({ control: methods.control, name: 'panelId' });
  const action = useWatch({ control: methods.control, name: 'action' });

  const inboundOptions = useMemo(() => {
    const panel = panels.find((p) => p.id === panelId);
    return (panel?.inbounds ?? []).map((inbound) => ({
      value: inbound.tag,
      label: inbound.remark ? `${inbound.remark} (${inbound.tag})` : inbound.tag,
    }));
  }, [panels, panelId]);

  const linkOptions = useMemo(() => {
    const nameById = new Map(panels.map((p) => [p.id, p.name || `panel ${p.id}`]));
    return links
      .filter((link) => link.sourcePanelId === panelId)
      .map((link) => ({
        value: link.id,
        label: `${link.sourceInboundTag} → ${nameById.get(link.targetPanelId) ?? link.targetPanelId}`,
      }));
  }, [links, panels, panelId]);

  async function onFinish(values: FilterRuleFormValues) {
    const result = FilterRuleFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.filters.toasts.fillRequired'));
      return;
    }
    const v = result.data;
    setSubmitting(true);
    try {
      const msg = await save({
        id: v.id || 0,
        name: v.name.trim(),
        remark: v.remark?.trim() || '',
        panelId: v.panelId,
        sourceInboundTags: v.sourceInboundTags,
        listIds: v.listIds,
        action: v.action,
        cascadeLinkId: v.action === 'cascade' ? v.cascadeLinkId : 0,
        sortOrder: v.sortOrder ?? 0,
        enable: v.enable,
      });
      if (msg?.success) onOpenChange(false);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <>
      {messageContextHolder}
      <Modal
        open={open}
        title={mode === 'edit' ? t('pages.filters.editRule') : t('pages.filters.addRule')}
        confirmLoading={submitting}
        okText={t('save')}
        cancelText={t('cancel')}
        mask={{ closable: false }}
        width="640px"
        onOk={methods.handleSubmit(onFinish)}
        onCancel={() => onOpenChange(false)}
      >
        <FormProvider {...methods}>
          <Form layout="vertical">
            <FormField
              label={t('pages.filters.name')}
              name="name"
              rules={{ validate: rhfZodValidate(FilterRuleFormSchema.shape.name) }}
            >
              <Input placeholder="block ads" />
            </FormField>
            <FormField label={t('pages.filters.remark')} name="remark">
              <Input />
            </FormField>
            <FormField label={t('pages.filters.rulePanel')} name="panelId">
              <Select
                options={panels.map((panel) => ({
                  value: panel.id,
                  label: panel.name || `panel ${panel.id}`,
                }))}
              />
            </FormField>
            <FormField
              label={t('pages.filters.ruleScope')}
              name="sourceInboundTags"
              extra={t('pages.filters.ruleScopeHint')}
            >
              <Select mode="multiple" allowClear options={inboundOptions} />
            </FormField>
            <FormField
              label={t('pages.filters.ruleLists')}
              name="listIds"
              rules={{ validate: rhfZodValidate(FilterRuleFormSchema.shape.listIds) }}
            >
              <Select
                mode="multiple"
                options={lists.map((list) => ({
                  value: list.id,
                  label: `${list.name} (${list.kind === 'ip' ? t('pages.filters.kindIp') : t('pages.filters.kindDomain')})`,
                }))}
              />
            </FormField>
            <FormField
              label={t('pages.filters.ruleAction')}
              name="action"
              extra={t('pages.filters.ruleActionHint')}
            >
              <Select
                options={[
                  { value: 'block', label: t('pages.filters.action_block') },
                  { value: 'direct', label: t('pages.filters.action_direct') },
                  { value: 'cascade', label: t('pages.filters.action_cascade') },
                ]}
              />
            </FormField>
            {action === 'cascade' && (
              <FormField
                label={t('pages.filters.ruleLink')}
                name="cascadeLinkId"
                extra={t('pages.filters.ruleLinkHint')}
              >
                <Select options={linkOptions} />
              </FormField>
            )}
            <FormField label={t('enable')} name="enable" valueProp="checked">
              <Switch />
            </FormField>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
