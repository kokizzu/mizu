import React, { useCallback, useEffect, useMemo, useState } from "react";
import styles from '../style/EntriesList.module.sass';
import ScrollableFeedVirtualized from "react-scrollable-feed-virtualized";
import Moment from 'moment';
import { EntryItem } from "./EntryListItem/EntryListItem";
import down from "assets/downImg.svg";
import spinner from 'assets/spinner.svg';
import { RecoilState, useRecoilState, useRecoilValue, useSetRecoilState } from "recoil";
import entriesAtom from "../../recoil/entries";
import queryAtom from "../../recoil/query";
import TrafficViewerApiAtom from "../../recoil/TrafficViewerApi";
import TrafficViewerApi from "./TrafficViewerApi";
import focusedEntryIdAtom from "../../recoil/focusedEntryId";
import { toast } from "react-toastify";
import { MAX_ENTRIES, TOAST_CONTAINER_ID } from "../../configs/Consts";
import tappingStatusAtom from "../../recoil/tappingStatus";
import leftOffTopAtom from "../../recoil/leftOffTop";
import { DEFAULT_LEFTOFF } from "../../helpers/Consts";
import { IsShouldStartStreamDataContext } from "./IsShouldStartStreamDataContext";

interface EntriesListProps {
  listEntryREF: any;
  onSnapBrokenEvent: () => void;
  isSnappedToBottom: boolean;
  setIsSnappedToBottom: any;
  noMoreDataTop: boolean;
  setNoMoreDataTop: (flag: boolean) => void;
  openEmptyWebSocket: (resetEntries: boolean, leftOffBottom?: string, queryToSend?: string) => void;
  scrollableRef: any;
  ws: any;
}

export const EntriesList: React.FC<EntriesListProps> = ({
  listEntryREF,
  onSnapBrokenEvent,
  isSnappedToBottom,
  setIsSnappedToBottom,
  noMoreDataTop,
  setNoMoreDataTop,
  openEmptyWebSocket,
  scrollableRef,
  ws
}) => {

  const [entries, setEntries] = useRecoilState(entriesAtom);
  const query = useRecoilValue(queryAtom);
  const isWsConnectionClosed = ws?.current?.readyState !== WebSocket.OPEN;
  const [focusedEntryId, setFocusedEntryId] = useRecoilState(focusedEntryIdAtom);
  const [leftOffTop, setLeftOffTop] = useRecoilState(leftOffTopAtom);
  const setTappingStatus = useSetRecoilState(tappingStatusAtom);

  const trafficViewerApi = useRecoilValue(TrafficViewerApiAtom as RecoilState<TrafficViewerApi>)

  const [loadMoreTop, setLoadMoreTop] = useState(false);
  const [isLoadingTop, setIsLoadingTop] = useState(false);
  const [queriedTotal, setQueriedTotal] = useState(0);
  const [startTime, setStartTime] = useState(0);
  const [truncatedTimestamp, setTruncatedTimestamp] = useState(0);

  const { setIsShouldStartStreamData, isShouldStartStreamData } = React.useContext(IsShouldStartStreamDataContext)

  const getLeftOffBottom = (entriesArr) => entriesArr.length > 0 ? entriesArr[entriesArr.length - 1].id : DEFAULT_LEFTOFF
  const leftOffBottom = useMemo(() => getLeftOffBottom(entries), [entries])
  const scrollbarVisible = scrollableRef.current?.childWrapperRef.current.clientHeight > scrollableRef.current?.wrapperRef.current.clientHeight;

  useEffect(() => {
    const list = document.getElementById('list').firstElementChild;
    const handleScroll = (e: any) => {
      if (e.target.scrollTop === 0) {
        setLoadMoreTop(true);
      } else {
        setNoMoreDataTop(false);
        setLoadMoreTop(false);
      }
    };
    list.addEventListener('scroll', handleScroll)
    return () => {
      list.removeEventListener('scroll', handleScroll)
    }
  }, [setLoadMoreTop, setNoMoreDataTop]);

  const memoizedEntries = useMemo(() => {
    return entries;
  }, [entries]);

  const getOldEntries = useCallback(async () => {
    setLoadMoreTop(false);
    const leftOffTopForFetch = leftOffTop === "" ? DEFAULT_LEFTOFF : leftOffTop
    setIsLoadingTop(true);
    const data = await trafficViewerApi.fetchEntries(leftOffTopForFetch, -1, query, 100, 3000);
    if (!data || data.data === null || data.meta === null) {
      setNoMoreDataTop(true);
      setIsLoadingTop(false);
      return [];
    }
    setLeftOffTop(data.meta.leftOff);

    let scrollTo: boolean;
    if (data.meta.noMoreData) {
      setNoMoreDataTop(true);
      scrollTo = false;
    } else {
      scrollTo = true;
    }
    setIsLoadingTop(false);
    const oldEntries = [...data.data.reverse()]
    const newEntries = [...oldEntries, ...entries];
    if (newEntries.length > MAX_ENTRIES) {
      newEntries.splice(MAX_ENTRIES, newEntries.length - MAX_ENTRIES)
    }
    setEntries(newEntries);

    setQueriedTotal(data.meta.total);
    setTruncatedTimestamp(data.meta.truncatedTimestamp);

    if (scrollTo) {
      scrollableRef.current.scrollToIndex(data.data.length - 1);
    }

    return oldEntries
  }, [trafficViewerApi, setLoadMoreTop, setIsLoadingTop, entries, setEntries, query, setNoMoreDataTop, leftOffTop, setLeftOffTop, setQueriedTotal, setTruncatedTimestamp, scrollableRef]);

  useEffect(() => {
    if (loadMoreTop && noMoreDataTop) {
      getOldEntries();
    }
  }, [loadMoreTop, noMoreDataTop, getOldEntries]);

  useEffect(() => {
    (async () => {
      if (isShouldStartStreamData && trafficViewerApi?.fetchEntries) {
        setEntries([])
        const oldEntries = await getOldEntries()
        // we calculate LeftOffBottom because we want to know from where to continue 
        //1) more records were added in the server during api call 
        //2) entries state wasnt updated yet
        openEmptyWebSocket(false, getLeftOffBottom(oldEntries))
        setIsShouldStartStreamData(false)
      }
    })();
  }, [isShouldStartStreamData, trafficViewerApi.fetchEntries]);

  useEffect(() => {
    if (!focusedEntryId && entries.length > 0)
      setFocusedEntryId(entries[0].id);
  }, [focusedEntryId, entries])

  useEffect(() => {
    const newEntries = [...entries];
    if (newEntries.length > MAX_ENTRIES) {
      setLeftOffTop(newEntries[0].id);
      newEntries.splice(0, newEntries.length - MAX_ENTRIES)
      setNoMoreDataTop(false);
      setEntries(newEntries);
    }
  }, [entries])

  if (ws.current && !ws.current.onmessage) {
    ws.current.onmessage = (e) => {
      if (!e?.data) return;
      const message = JSON.parse(e.data);
      switch (message.messageType) {
        case "entry":
          setEntries((entriesState) => {
            // websocket returns 1 last entry which can exist already when open socket after fetch
            const isExistAlready = entriesState.find(x => x.id === message.data.id)
            return !isExistAlready ? [...entriesState, message.data] : entriesState
          });
          break;
        case "status":
          setTappingStatus(message.tappingStatus);
          break;
        case "toast":
          toast[message.data.type](message.data.text, {
            theme: "colored",
            autoClose: message.data.autoClose,
            pauseOnHover: true,
            progress: undefined,
            containerId: TOAST_CONTAINER_ID
          });
          break;
        case "queryMetadata":
          setTruncatedTimestamp(message.data.truncatedTimestamp);
          setQueriedTotal(message.data.total);
          setLeftOffTop(leftOffState => leftOffState === "" ? message.data.leftOff : leftOffState);
          break;
        case "startTime":
          setStartTime(message.data);
          break;
      }
    }
  }

  return <React.Fragment>
    <div className={styles.list}>
      <div id="list" ref={listEntryREF} className={styles.list}>
        {isLoadingTop && <div className={styles.spinnerContainer}>
          <img alt="spinner" src={spinner} style={{ height: 25 }} />
        </div>}
        {noMoreDataTop && <div id="noMoreDataTop" className={styles.noMoreDataAvailable}>No more data available</div>}
        <ScrollableFeedVirtualized ref={scrollableRef} itemHeight={48} marginTop={10} onSnapBroken={onSnapBrokenEvent}>
          {false /* It's because the first child is ignored by ScrollableFeedVirtualized */}
          {memoizedEntries.map(entry => <EntryItem
            key={`entry-${entry.id}`}
            entry={entry}
            style={{}}
            headingMode={false}
          />)}
        </ScrollableFeedVirtualized>
        <button type="button"
          title="Fetch old records"
          className={`${styles.btnOld} ${!scrollbarVisible && (leftOffTop !== "") ? styles.showButton : styles.hideButton}`}
          onClick={(_) => {
            trafficViewerApi.webSocket.close()
            getOldEntries();
          }}>
          <img alt="down" src={down} />
        </button>
        <button type="button"
          title="Snap to bottom"
          className={`${styles.btnLive} ${isSnappedToBottom && !isWsConnectionClosed ? styles.hideButton : styles.showButton}`}
          onClick={(_) => {
            if (isWsConnectionClosed) {
              openEmptyWebSocket(false, leftOffBottom)
            }
            scrollableRef.current.jumpToBottom();
            setIsSnappedToBottom(true);
          }}>
          <img alt="down" src={down} />
        </button>
      </div>

      <div className={styles.footer}>
        <div>Displaying <b id="entries-length">{entries?.length > MAX_ENTRIES ? MAX_ENTRIES : entries?.length}</b> results out of <b
          id="total-entries">{queriedTotal}</b> total
        </div>
        {startTime !== 0 && <div>First traffic entry time <span style={{
          marginRight: 5,
          fontWeight: 600,
          fontSize: 13
        }}>{Moment(truncatedTimestamp ? truncatedTimestamp : startTime).utc().format('MM/DD/YYYY, h:mm:ss.SSS A')}</span>
        </div>}
      </div>
    </div>
  </React.Fragment>;
};

export default EntriesList;