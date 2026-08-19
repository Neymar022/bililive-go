import { mediaDisplayList, mediaDisplayName, mediaDisplayTitle } from './media-display';

test('uses the API display name while preserving the raw path key', () => {
  const file = {
    name: '主播.S01E1673386692282296.2026-08-18 - 房间标题.mp4',
    display_name: '2026-08-18 - 房间标题',
  };

  expect(mediaDisplayName(file)).toBe('2026-08-18 - 房间标题');
  expect(file.name).toContain('S01E1673386692282296');
});

test('hides the identity for pipeline file paths without changing the path', () => {
  const path = '/video/主播/Season 01/主播.S01E1673386692282296.2026-08-18 - 房间标题.mp4';

  expect(mediaDisplayTitle(path)).toBe('2026-08-18 - 房间标题');
  expect(path).toContain('S01E1673386692282296');
});

test('keeps unmatched basenames unchanged', () => {
  expect(mediaDisplayTitle('/video/cover.jpg')).toBe('cover.jpg');
});

test('does not rewrite legacy short episode numbers', () => {
  expect(mediaDisplayTitle('/video/主播.S01E0047.2026-05-27 - 房间标题.mp4')).toBe(
    '主播.S01E0047.2026-05-27 - 房间标题.mp4'
  );
});

test('formats batch confirmation labels without exposing recordedAt identities', () => {
  const files = [
    { name: '主播.S01E1673386692282296.2026-08-18 - 第一场.mp4' },
    { name: '主播.S01E1673473092282296.2026-08-19 - 第二场.mp4' },
  ];

  expect(mediaDisplayList(files)).toBe('2026-08-18 - 第一场、2026-08-19 - 第二场');
  expect(files[0].name).toContain('S01E1673386692282296');
});
