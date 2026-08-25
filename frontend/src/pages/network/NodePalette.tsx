import { useTranslation } from 'react-i18next';
import { Typography } from 'antd';
import { FilterOutlined, GlobalOutlined, ImportOutlined, StopOutlined } from '@ant-design/icons';

export type PaletteKind = 'source' | 'filter' | 'block' | 'direct';

const ITEMS: { kind: PaletteKind; icon: React.ReactNode; labelKey: string; hintKey: string }[] = [
  {
    kind: 'source',
    icon: <ImportOutlined />,
    labelKey: 'pages.network.palette.source',
    hintKey: 'pages.network.palette.sourceHint',
  },
  {
    kind: 'filter',
    icon: <FilterOutlined />,
    labelKey: 'pages.network.palette.filter',
    hintKey: 'pages.network.palette.filterHint',
  },
  {
    kind: 'block',
    icon: <StopOutlined />,
    labelKey: 'pages.network.sinkBlock',
    hintKey: 'pages.network.palette.blockHint',
  },
  {
    kind: 'direct',
    icon: <GlobalOutlined />,
    labelKey: 'pages.network.sinkDirect',
    hintKey: 'pages.network.palette.directHint',
  },
];

// The shelf under the canvas: drag a kind out of it, drop it where it belongs,
// then wire it up.
export default function NodePalette() {
  const { t } = useTranslation();

  return (
    <div className="node-palette">
      <Typography.Text type="secondary" className="node-palette-title">
        {t('pages.network.palette.title')}
      </Typography.Text>
      <div className="node-palette-items">
        {ITEMS.map((item) => (
          <div
            key={item.kind}
            className={`node-palette-item is-${item.kind}`}
            draggable
            title={t(item.hintKey)}
            onDragStart={(event) => {
              event.dataTransfer.setData('application/x-network-node', item.kind);
              event.dataTransfer.effectAllowed = 'move';
            }}
          >
            {item.icon}
            <span>{t(item.labelKey)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
