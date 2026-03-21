import React from 'react';
import { render } from '@testing-library/react';
import { act } from 'react-dom/test-utils';
import * as ConfigInfoModule from './index';

jest.mock('antd', () => {
  const React = require('react');

  const Select = ({ value, onChange, options = [] }: any) => (
    <select value={value} onChange={(event) => onChange(event.target.value)}>
      {options.map((option: any) => (
        <option key={option.value} value={option.value}>{option.label}</option>
      ))}
    </select>
  );
  Select.Option = ({ value, children }: any) => <option value={value}>{children}</option>;

  const Form = ({ children }: any) => <form>{children}</form>;
  Form.useForm = () => [{
    getFieldsValue: jest.fn(() => ({})),
    setFieldsValue: jest.fn(),
    resetFields: jest.fn(),
    validateFields: jest.fn(async () => ({})),
  }];
  Form.Item = ({ children }: any) => <div>{children}</div>;
  Form.List = ({ children }: any) => children([], { add: jest.fn(), remove: jest.fn() });

  const Input = ({ value, onChange, ...props }: any) => (
    <input value={value} onChange={onChange} {...props} />
  );
  Input.TextArea = ({ value, onChange, ...props }: any) => (
    <textarea value={value} onChange={onChange} {...props} />
  );
  Input.Password = ({ value, onChange, ...props }: any) => (
    <input type="password" value={value} onChange={onChange} {...props} />
  );
  Input.Search = ({ value, onChange, onSearch, ...props }: any) => (
    <input
      value={value}
      onChange={onChange}
      onKeyDown={(event: any) => {
        if (event.key === 'Enter' && onSearch) {
          onSearch(event.currentTarget.value);
        }
      }}
      {...props}
    />
  );

  const Collapse = ({ children }: any) => <div>{children}</div>;
  Collapse.Panel = ({ children }: any) => <div>{children}</div>;

  const Tabs = ({ items = [] }: any) => (
    <div>
      {items.map((item: any) => (
        <section key={item.key}>
          <h2>{typeof item.label === 'string' ? item.label : item.key}</h2>
          {item.children}
        </section>
      ))}
    </div>
  );

  const List = ({ children }: any) => <div>{children}</div>;
  List.Item = ({ children }: any) => <div>{children}</div>;

  return {
    Alert: ({ children, message, description }: any) => <div>{children || message || description}</div>,
    Badge: ({ children }: any) => <div>{children}</div>,
    Button: ({ children, onClick, loading, ...props }: any) => (
      <button onClick={onClick} {...props}>{loading ? 'loading' : children}</button>
    ),
    Card: ({ title, children }: any) => <section><h3>{title}</h3>{children}</section>,
    Collapse,
    Divider: ({ children }: any) => <div>{children}</div>,
    Form,
    Input,
    InputNumber: ({ value, onChange, ...props }: any) => (
      <input
        type="number"
        value={value}
        onChange={(event) => onChange?.(Number(event.target.value))}
        {...props}
      />
    ),
    List,
    Modal: { confirm: jest.fn() },
    Select,
    Space: ({ children }: any) => <div>{children}</div>,
    Spin: () => <div>loading</div>,
    Switch: ({ checked, onChange }: any) => (
      <input type="checkbox" checked={checked} onChange={(event) => onChange?.(event.target.checked)} />
    ),
    Tabs,
    Tag: ({ children }: any) => <span>{children}</span>,
    Tooltip: ({ children }: any) => <div>{children}</div>,
    Typography: {
      Paragraph: ({ children }: any) => <p>{children}</p>,
      Text: ({ children }: any) => <span>{children}</span>,
      Title: ({ children }: any) => <h2>{children}</h2>,
    },
    message: {
      error: jest.fn(),
      success: jest.fn(),
    },
  };
});

jest.mock('@ant-design/icons', () => {
  const React = require('react');
  const Icon = () => React.createElement('span');
  return {
    SettingOutlined: Icon,
    GlobalOutlined: Icon,
    AppstoreOutlined: Icon,
    BellOutlined: Icon,
    LinkOutlined: Icon,
    InfoCircleOutlined: Icon,
    SaveOutlined: Icon,
    ReloadOutlined: Icon,
    EditOutlined: Icon,
    DeleteOutlined: Icon,
    RightOutlined: Icon,
    PlusOutlined: Icon,
    WarningOutlined: Icon,
    ExclamationCircleOutlined: Icon,
    MobileOutlined: Icon,
    FileTextOutlined: Icon,
    CloudUploadOutlined: Icon,
  };
});

jest.mock('react-router-dom', () => ({
  Link: ({ children, to }: any) => <a href={to}>{children}</a>,
  useLocation: () => ({ pathname: '/configInfo', search: '', hash: '' }),
}));

jest.mock('react-simple-code-editor', () => () => <div>editor</div>);

jest.mock('prismjs', () => ({
  highlight: jest.fn((value: string) => value),
  languages: { yaml: {} },
}));

jest.mock('prismjs/components/prism-yaml', () => ({}));

jest.mock('./shared-fields', () => ({
  OutputTemplatePreview: () => <div>preview</div>,
  getFFmpegInheritance: jest.fn(() => ({
    source: 'default',
    linkTo: '',
    isOverridden: false,
    inheritedValue: '',
  })),
  getFFmpegDisplayValue: jest.fn(() => ''),
}));

jest.mock('./CloudUploadSettings', () => () => <div>cloud upload settings</div>);

jest.mock('../../utils/api', () => {
  return jest.fn().mockImplementation(() => ({}));
});

describe('SubtitleSettingsPanel', () => {
  beforeEach(() => {
    global.fetch = jest.fn() as any;
  });

  afterEach(() => {
    jest.resetAllMocks();
  });

  test('shows default preset and saves updates', async () => {
    const SubtitleSettingsPanel = (ConfigInfoModule as any).SubtitleSettingsPanel;
    expect(SubtitleSettingsPanel).toBeDefined();
    if (!SubtitleSettingsPanel) {
      return;
    }

    const mockFetch = global.fetch as jest.Mock;
    mockFetch
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          err_no: 0,
          data: {
            subtitle: {
              enabled: true,
              burn_style: {
                preset: 'vizard_classic_cn',
              },
            },
            source_root: '/srv/bililive-source',
            library_root: '/srv/bililive',
            worker_url: 'http://subtitle-worker:8091',
          },
        }),
      })
      .mockResolvedValueOnce({
        ok: true,
        json: async () => ({
          err_no: 0,
          data: 'OK',
        }),
      });

    const view = render(<SubtitleSettingsPanel />);

    expect(await view.findByText('字幕渲染预设')).toBeInTheDocument();
    const presetSelect = view.getByDisplayValue('vizard_classic_cn') as HTMLSelectElement;
    expect(presetSelect).toBeInTheDocument();

    presetSelect.value = 'vizard_classic_cn';
    presetSelect.dispatchEvent(new Event('change', { bubbles: true }));
    await act(async () => {
      view.getByText('保存字幕设置').click();
      await Promise.resolve();
      await Promise.resolve();
    });

    expect(mockFetch).toHaveBeenNthCalledWith(2, '/api/subtitles/settings', expect.objectContaining({
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: expect.stringContaining('"preset":"vizard_classic_cn"'),
    }));
  });
});
