/** Export and thumbnail generation share the backend's FFmpeg availability probe. */
export function isFFmpegAvailable(value?: { available?: boolean } | null): boolean {
  return value?.available === true
}
