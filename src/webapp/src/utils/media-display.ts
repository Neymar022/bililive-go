export interface MediaDisplayRecord {
  name: string;
  display_name?: string;
}

const libraryEpisodeName = /^.+?\.S\d+E\d+(?:-S\d+E\d+)?\.(\d{4}-\d{2}-\d{2})\s+-\s+(.+?)(?:\.[^.]+)?$/;
const normalizedRecordingName = /^.+?\s+-\s+(\d{4}-\d{2}-\d{2}\s+\d{2}-\d{2}-\d{2})\s+-\s+(.+?)(?:\.[^.]+)?$/;

export function mediaDisplayTitle(path: string): string {
  const name = path.split(/[/\\]/).pop() || path;
  const match = name.match(libraryEpisodeName) || name.match(normalizedRecordingName);
  if (match) {
    return `${match[1]} - ${match[2]}`;
  }
  return name;
}

export function mediaDisplayName(record: MediaDisplayRecord): string {
  return record.display_name || mediaDisplayTitle(record.name);
}
