import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Form, Modal, Select, Typography } from 'antd';

import type { GraphPanel } from '@/schemas/network';

interface SourcePickerModalProps {
  open: boolean;
  panels: GraphPanel[];
  onCancel: () => void;
  onPick: (panelId: number, tags: string[]) => void;
}

// Asks the two things an inbound source node is: whose inbounds, and which.
export default function SourcePickerModal({
  open,
  panels,
  onCancel,
  onPick,
}: SourcePickerModalProps) {
  const { t } = useTranslation();
  const [panelId, setPanelId] = useState<number>(panels[0]?.id ?? 0);
  const [tags, setTags] = useState<string[]>([]);

  // Reset during render, not in an effect, so the first frame is already clean.
  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setPanelId(panels[0]?.id ?? 0);
      setTags([]);
    }
  }

  const inboundOptions = useMemo(() => {
    const panel = panels.find((p) => p.id === panelId);
    return (panel?.inbounds ?? []).map((inbound) => ({
      value: inbound.tag,
      label: inbound.remark ? `${inbound.remark} (${inbound.tag})` : inbound.tag,
    }));
  }, [panels, panelId]);

  return (
    <Modal
      open={open}
      title={t('pages.network.palette.source')}
      okText={t('pages.network.addToCanvas')}
      cancelText={t('cancel')}
      onOk={() => onPick(panelId, tags)}
      onCancel={onCancel}
      destroyOnHidden
    >
      <Form layout="vertical">
        <Form.Item label={t('pages.filters.rulePanel')}>
          <Select
            value={panelId}
            onChange={(next) => {
              setPanelId(next);
              setTags([]);
            }}
            options={panels.map((panel) => ({
              value: panel.id,
              label: panel.name || `panel ${panel.id}`,
            }))}
          />
        </Form.Item>
        <Form.Item label={t('pages.filters.ruleScope')}>
          <Select
            mode="multiple"
            allowClear
            value={tags}
            onChange={setTags}
            options={inboundOptions}
            placeholder={t('pages.filters.allInbounds')}
          />
        </Form.Item>
        <Typography.Text type="secondary">{t('pages.filters.ruleScopeHint')}</Typography.Text>
      </Form>
    </Modal>
  );
}
