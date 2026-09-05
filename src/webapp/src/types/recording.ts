export type RecordingMode = 'once' | 'continuous';

export const recordingModeOptions = [
    { value: 'once', label: '单次录制' },
    { value: 'continuous', label: '连续录制' },
];

export function recordingStateLabel(state?: string): string | undefined {
    switch (state) {
        case 'completed': return '单次已完成';
        case 'paused': return '待确认';
        case 'finalizing': return '录制收尾中';
        default: return undefined;
    }
}

export function recordingPauseReasonLabel(reason?: string): string | undefined {
    switch (reason) {
        case 'unconfirmed_session_after_restart': return '重启后场次归属未确认';
        case 'media_verification_failed': return '录制产物尚未通过播放验证';
        case 'once_completed': return '本轮单次录制已完成';
        case 'user_stop': return '用户已停止本轮录制';
        default: return reason;
    }
}
