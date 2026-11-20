\set ON_ERROR_STOP on
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout='15s';
SELECT jsonb_build_object(
  'schema_version',2,
  'adapter_version','ugreen-fixed-select-v2',
  'generated_at',clock_timestamp(),
  'library_root',:'library_root',
  'libraries',COALESCE((SELECT jsonb_agg(jsonb_build_object(
    'id',l.media_lib_set_id,'roots',l.media_lib_folder_arr,
    'read_local_info_first',l.read_local_info_first,
    'nfo_info_save_to_local',l.nfo_info_save_to_local
  )) FROM media_lib_set l WHERE l.media_lib_set_id=4),'[]'::jsonb),
  'files',COALESCE((SELECT jsonb_agg(jsonb_build_object(
    'path',f.file_path,'file_id',f.file_id,'file_size',f.file_size,
    'category_id',f.category_id,'library_id',f.media_lib_set_id,
    'season',f.season_num,'episode',f.episode_num,
    'ordinal_file_count',(SELECT count(*) FROM file_info q WHERE q.category_id=f.category_id AND q.season_num=f.season_num AND q.episode_num=f.episode_num),
    'audio_count',(SELECT count(*) FROM file_audio_stream a WHERE a.file_id=f.file_id),
    'video_count',(SELECT count(*) FROM file_video_stream v WHERE v.file_id=f.file_id),
    'episodes',COALESCE((SELECT jsonb_agg(jsonb_build_object(
      'id',e.ug_television_episode_id,'category_id',e.category_id,
      'season',e.season,'episode',e.episode,'library_id',e.media_lib_set_id,
      'paths',e.file_path
    )) FROM ug_television_episode e WHERE e.category_id=f.category_id AND e.season=f.season_num AND e.episode=f.episode_num),'[]'::jsonb),
    'shows',COALESCE((SELECT jsonb_agg(jsonb_build_object(
      'id',v.ug_video_info_id,'name',v.name,'category_id',v.category_id,
      'library_id',v.media_lib_set_id
    )) FROM ug_video_info v WHERE v.category_id=f.category_id),'[]'::jsonb)
  ) ORDER BY f.file_path,f.file_id) FROM file_info f
    WHERE left(f.file_path, length(:'library_root') + 1) = :'library_root' || '/'
  ),'[]'::jsonb)
);
COMMIT;
