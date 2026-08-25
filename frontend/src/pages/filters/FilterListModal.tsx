import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Form, Input, Modal, Select, Switch, message } from 'antd';
import { FormProvider, useForm } from 'react-hook-form';

import type { Msg } from '@/utils';
import { FilterListFormSchema, type FilterList, type FilterListFormValues } from '@/schemas/filter';
import { FormField, rhfZodValidate } from '@/components/form/rhf';

interface FilterListModalProps {
  open: boolean;
  mode: 'add' | 'edit';
  list: FilterList | null;
  save: (payload: Partial<FilterList>) => Promise<Msg<unknown>>;
  onOpenChange: (open: boolean) => void;
}

function defaultValues(): FilterListFormValues {
  return { id: 0, name: '', remark: '', kind: 'domain', entriesText: '', enable: true };
}

export default function FilterListModal({
  open,
  mode,
  list,
  save,
  onOpenChange,
}: FilterListModalProps) {
  const { t } = useTranslation();
  const methods = useForm<FilterListFormValues>({ defaultValues: defaultValues() });
  const [messageApi, messageContextHolder] = message.useMessage();
  const [submitting, setSubmitting] = useState(false);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [synced, setSynced] = useState<{ mode: string; list: FilterList | null } | null>(null);
  if (!open) {
    if (synced) setSynced(null);
  } else if (!synced || synced.mode !== mode || synced.list !== (list ?? null)) {
    setSynced({ mode, list: list ?? null });
    const base = defaultValues();
    methods.reset(
      mode === 'edit' && list
        ? {
            ...base,
            id: list.id,
            name: list.name,
            remark: list.remark ?? '',
            kind: list.kind ?? 'domain',
            entriesText: (list.entries ?? []).join('\n'),
            enable: list.enable ?? true,
          }
        : base,
    );
  }

  const title = useMemo(
    () => (mode === 'edit' ? t('pages.filters.editList') : t('pages.filters.addList')),
    [mode, t],
  );

  async function onFinish(values: FilterListFormValues) {
    const result = FilterListFormSchema.safeParse(values);
    if (!result.success) {
      messageApi.error(t(result.error.issues[0]?.message ?? 'pages.filters.toasts.fillRequired'));
      return;
    }
    setSubmitting(true);
    try {
      const msg = await save({
        id: result.data.id || 0,
        name: result.data.name.trim(),
        remark: result.data.remark?.trim() || '',
        kind: result.data.kind,
        entries: result.data.entriesText
          .split('\n')
          .map((entry) => entry.trim())
          .filter(Boolean),
        enable: result.data.enable,
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
        title={title}
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
              rules={{ validate: rhfZodValidate(FilterListFormSchema.shape.name) }}
            >
              <Input placeholder="ads" />
            </FormField>
            <FormField label={t('pages.filters.remark')} name="remark">
              <Input />
            </FormField>
            <FormField
              label={t('pages.filters.kind')}
              name="kind"
              extra={t('pages.filters.kindHint')}
            >
              <Select
                options={[
                  { value: 'domain', label: t('pages.filters.kindDomain') },
                  { value: 'ip', label: t('pages.filters.kindIp') },
                ]}
              />
            </FormField>
            <FormField
              label={t('pages.filters.entries')}
              name="entriesText"
              extra={t('pages.filters.entriesHint')}
            >
              <Input.TextArea rows={10} placeholder={'geosite:category-ads-all\ndoubleclick.net'} />
            </FormField>
            <FormField label={t('enable')} name="enable" valueProp="checked">
              <Switch />
            </FormField>
          </Form>
        </FormProvider>
      </Modal>
    </>
  );
}
