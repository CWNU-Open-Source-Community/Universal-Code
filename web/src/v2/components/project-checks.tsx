import { useState } from "react";
import type { APIClient } from "../../api/client";
import { ProjectActions } from "./project-actions";
import { ProjectPreparation } from "./project-preparation";

interface ProjectChecksProps {
  client: APIClient;
  runID: string;
  threadID: string;
  working?: boolean;
  onFeedback?: (context: string) => void;
}

export function ProjectChecks(props: ProjectChecksProps) {
  return <ProjectChecksForRun key={props.client.baseURL + ":" + props.runID} {...props} />;
}

function ProjectChecksForRun(props: ProjectChecksProps) {
  const [testTarget, setTestTarget] = useState("");
  return <>
    <ProjectPreparation {...props} onPrepared={setTestTarget} />
    <ProjectActions {...props} testTarget={testTarget} onTestTargetChange={setTestTarget} />
  </>;
}
