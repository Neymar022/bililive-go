import React from 'react';
import { render } from '@testing-library/react';
import { act, Simulate } from 'react-dom/test-utils';
import AddRoomDialog from './index';
import Utils from '../../utils/common';

jest.mock('antd', () => {
    const React = require('react');
    return {
        Modal: ({ open, children, onOk }: any) => open ? <section>{children}<button onClick={onOk}>OK</button></section> : null,
        Input: ({ value, onChange, placeholder }: any) => <input value={value} onChange={onChange} placeholder={placeholder} />,
        Radio: { Group: ({ options, value, onChange }: any) => <div>{options.map((option: any) => (
            <label key={option.value}><input type="radio" checked={value === option.value} onChange={() => onChange({ target: { value: option.value } })} />{option.label}</label>
        ))}</div> },
    };
});

afterEach(() => jest.restoreAllMocks());

test('新增默认单次，选择连续后请求传递连续模式，再次打开恢复单次默认', async () => {
    const post = jest.spyOn(Utils.prototype, 'requestPost').mockResolvedValue([]);
    jest.spyOn(Utils.prototype, 'requestPut').mockResolvedValue({ err_no: 0 });
    const ref = React.createRef<AddRoomDialog>();
    const view = render(<AddRoomDialog ref={ref} refresh={jest.fn()} />);
    act(() => ref.current!.showModal());
    expect(view.getByLabelText('单次录制')).toBeChecked();
    const input = view.getByPlaceholderText('https://') as HTMLInputElement;
    act(() => { input.value = 'https://live.bilibili.com/1'; Simulate.change(input); });
    act(() => view.getByLabelText('连续录制').click());
    await act(async () => { view.getByText('OK').click(); await Promise.resolve(); });
    expect(post).toHaveBeenCalledWith('api/lives', [{
        url: 'https://live.bilibili.com/1', listen: true, recording_mode: 'continuous',
    }]);
    act(() => ref.current!.showModal());
    expect(view.getByLabelText('单次录制')).toBeChecked();
});
