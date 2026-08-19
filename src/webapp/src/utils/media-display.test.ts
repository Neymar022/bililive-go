import { mediaDisplayName, mediaDisplayTitle } from './media-display';

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
