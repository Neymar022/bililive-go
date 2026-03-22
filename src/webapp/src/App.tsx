import React from 'react';
import './App.css';
import { Routes, Route } from 'react-router-dom';
import RootLayout from './component/layout/index';
import LiveList from './component/live-list/index';
import LiveInfo from './component/live-info/index';
import ConfigInfo from './component/config-info/index';
import FileList from './component/file-list/index';
import TaskPage from './component/task-page/index';
import IOStats from './component/io-stats/index';
import UpdateBanner from './component/update-banner/index';
import UpdatePage from './component/update-page/index';
import RecordingsPage from './component/recordings-page/index';

const SubtitleStyleLabPlaceholder: React.FC = () => <div>字幕样式实验室</div>;

const App: React.FC = () => {
  return (
    <>
      <UpdateBanner />
      <RootLayout>
        <Routes>
          <Route path="/update/*" element={<UpdatePage />} />
          <Route path="/iostats/*" element={<IOStats />} />
          <Route path="/tasks/*" element={<TaskPage />} />
          <Route path="/recordings/*" element={<RecordingsPage />} />
          <Route path="/subtitle-style-lab/*" element={<SubtitleStyleLabPlaceholder />} />
          <Route path="/fileList/*" element={<FileList />} />
          <Route path="/configInfo/*" element={<ConfigInfo />} />
          <Route path="/liveInfo" element={<LiveInfo />} />
          <Route path="/" element={<LiveList />} />
        </Routes>
      </RootLayout>
    </>
  );
}

export default App;
